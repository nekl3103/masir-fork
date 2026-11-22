// Masir is the desktop app for the masir daemon: a tray icon whose panel turns masir on
// and off, picks the server, and shows the rules. One tray icon on every OS: its panel is
// the system's popover on macOS, and a window of its own elsewhere.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/egoist/mygo"

	coreanalytics "github.com/Gozargah/Masir/libs/masir-core/analytics"
	"github.com/Gozargah/Masir/libs/masir-core/client"
	"github.com/Gozargah/Masir/libs/masir-core/paths"
	"github.com/Gozargah/Masir/libs/masir-core/service"
)

// version is set at build time: -X main.version=...
var version = "dev"

// shell holds what only exists with a running app: the tray and the panel.
type shell struct {
	a        *app
	tray     *mygo.Tray
	win      *mygo.Window // the open popover, or the window while it is hidden
	open     bool
	quitting bool

	// The window's size, coalesced: a transition asks every frame while it moves, and the
	// shell applies the latest ask after the frame.
	wantHeight int
	lastHeight int
	sizeQueued bool

	// frameSize says the window follows the animation every frame, as both a window of its
	// own and the macOS popover do.
	frameSize bool
}

func (s *shell) toggle() {
	if s.open {
		s.hide()
		return
	}
	s.show()
}

// resetForOpen puts the panel back on the main page with its lists closed. The height it
// opens at must be asked for after this, or a panel hidden on Settings (or with a list
// open) would open at that page's height and shrink once the main page drew.
func (a *app) resetForOpen() {
	a.resetRouter()
	a.path, a.open, a.lastSub = mainPath, sectionNone, mainPath
	a.subCustomInterval = 0
	a.scrollToSubID = ""
	a.motion = motion{instant: a.motion.instant} // a new panel starts at rest
	clear(a.warmed)                              // a new window shapes its pages again
	a.lastMotion = time.Time{}
	a.lastSize = time.Time{}
}

// start resets the panel to the main page, returns the height that page opens at, and
// follows the daemon's traffic while it shows.
func (s *shell) start() float32 {
	s.a.resetForOpen()
	s.a.frameSize = s.frameSize
	s.a.onHeight = s.setHeight
	height := s.a.heightForOpen()
	s.a.watchTraffic()
	return height
}

// stop stops the traffic stream when the panel goes away, and puts the router
// back at the main page with no history: closing the popover or the window is
// not a page to come back to, so the next open starts at "/".
func (s *shell) stop() {
	s.a.stopTraffic()
	s.a.resetRouter()
}

// setHeight resizes the panel once, after the frame that asked: never from inside a
// frame, and never while something moves. The view asks at rest points only, so the
// window and its content stay together.
func (s *shell) setHeight(h float32) {
	if s.win == nil || !s.open || h <= 0 {
		return
	}
	s.wantHeight = int(round32(h))
	if s.wantHeight == s.lastHeight || s.sizeQueued {
		return
	}
	s.sizeQueued = true
	w := s.win
	w.Update(func() {
		s.sizeQueued = false
		if s.win != w || !s.open || s.wantHeight == s.lastHeight {
			return
		}
		s.lastHeight = s.wantHeight
		w.SetContentSize(panelWidth, s.wantHeight)
	})
}

// sync follows the state: the panel's redraw and the tray's icon and tooltip.
func (s *shell) sync() {
	if s.win != nil {
		s.win.Invalidate()
	}
	s.a.updateTray(s.tray)
}

// ready puts the tray in place and hooks the app's life. A missing tray is not fatal:
// without one (some Linux desktops), the window is still the way in.
func (s *shell) ready() {
	if icon, err := trayIcon(false); err != nil {
		log.Print("tray icon: ", err)
	} else if t, err := mygo.NewTray(mygo.TrayOptions{
		ID:             "masir",
		Icon:           icon,
		IconIsTemplate: runtime.GOOS == "darwin", // the menu bar tints its own; elsewhere the icon keeps its color
		ToolTip:        "Masir Fork",
	}); err != nil {
		log.Print("tray: ", err)
	} else {
		s.tray = t
		if runtime.GOOS == "linux" {
			// AppIndicator shows the menu on a click; panels that report activation
			// (KDE's, GNOME's with an extension) call OnClick, which opens the window.
			s.tray.SetMenu(s.trayMenu())
			s.tray.OnClick(s.toggle)
		} else {
			s.tray.OnClick(s.toggle)
			s.tray.OnRightClick(func() {
				s.beforeTrayMenu()
				s.tray.SetMenu(s.trayMenu()) // for this popup only, so clicks keep toggling the panel
				defer s.tray.SetMenu(nil)
				s.tray.PopUpMenu()
			})
		}
	}
	s.platformReady()
	// A quit starts before its windows close, and the window's OnClose hides it into the
	// tray unless this is a real quit. The flag therefore has to be set in OnBeforeQuit:
	// setting it in OnWillQuit (which runs after the windows close) meant the close was
	// held back and the whole quit cancelled, so Exit in the tray did nothing on Windows
	// and Linux. macOS has no such window, which is why it worked there.
	mygo.App.OnBeforeQuit(func(*mygo.QuitEvent) {
		s.quitting = true
		st := &s.a.updates.state
		if st.phase == updateReady && st.appSync {
			// Discard the verified, uncommitted MyGo payload before the process exits.
			st.appCancel()
			<-st.appInstallDone
		}
		s.a.flushPanelHeight()
		s.a.flushGUISettings()
	})
	mygo.App.OnWillQuit(func(*mygo.QuitEvent) {
		// Quitting for good leaves nothing to turn masir off later, so this goes as
		// `masir disconnect` does. Closing the window into the tray is not a quit,
		// and neither is an update's relaunch; disconnectOnQuit knows the difference.
		s.a.disconnectOnQuit()
		if s.tray != nil {
			s.tray.Destroy()
		}
	})
}

// daemonBinary is the masir executable the app starts a daemon from: the one shipped
// in the app, MASIR_BIN, or masir on the PATH.
func daemonBinary() string {
	if p := os.Getenv("MASIR_BIN"); p != "" {
		return p
	}
	// A service has its own copy of masir and its own geo data. Start that when a
	// service is installed, even if it isn't running: the app should not bring up a
	// second daemon from the bundle with different data.
	if serviceDefined() {
		if p := service.BinaryPath(); fileExists(p) {
			return p
		}
	}
	if p := bundledBinary(); p != "" {
		return p
	}
	name := "masir"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	return ""
}

// bundledBinary is the masir the app ships with, even when another is on the PATH: the
// commands the app installs should be this exact build.
func bundledBinary() string {
	for _, p := range bundledBinaryPaths() {
		if fileExists(p) {
			return p
		}
	}
	return ""
}

func bundledBinaryPaths() []string {
	name := "masir"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	dir, err := mygo.App.Path(mygo.PathResources)
	if err != nil {
		return nil
	}
	// In bin: on Windows a file called masir.exe would be the app's own Masir.exe.
	return []string{filepath.Join(dir, "bin", name), filepath.Join(dir, name)}
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

// demoCycle goes between the main page and settings on repeat, for watching the
// transition and its window resizing when no one is there to click.
func demoCycle(s *shell) {
	const n = 12
	for i := 0; i < n; i++ {
		time.Sleep(1200 * time.Millisecond)
		mygo.RunOnMain(func() {
			if !s.open {
				s.show()
			}
			s.a.openPage(settingsPath)
			s.sync()
		})
		time.Sleep(1400 * time.Millisecond)
		mygo.RunOnMain(func() {
			s.a.goBack()
			s.sync()
		})
	}
	mygo.RunOnMain(func() {
		if !s.open {
			s.show()
		}
		s.a.goBack()
		s.sync()
	})
	log.Printf("demo: %d transitions done", n)
}

func round32(f float32) float32 {
	return float32(int(f + 0.5))
}

func main() {
	defer func() {
		if r := recover(); r != nil {
			if dir, err := paths.ConfigDir(); err == nil && coreanalytics.Allowed(dir, version) {
				_ = coreanalytics.WriteSpool(dir, coreanalytics.Event{Event: "gui_panic", Properties: map[string]any{"message": coreanalytics.SanitizeMessage(fmt.Sprint(r)), "component": "gui", "app_version": version}, CreatedAt: time.Now().UTC()})
			}
			panic(r)
		}
	}()

	name := "Masir"
	if n := os.Getenv("MASIR_APP_NAME"); n != "" {
		// A second copy for development: its own tray, single-instance lock and data.
		name = n
	}
	mygo.App.SetName(name)
	mygo.App.SetLanguages("en", "ru")
	if runtime.GOOS == "darwin" {
		// The panel is the app's only window: keep running as it closes.
		mygo.App.SetActivationPolicy(mygo.ActivationPolicyAccessory)
	}
	mygo.App.OnWindowAllClosed(func() {})
	if err := mygo.App.RegisterURLScheme("masir"); err != nil {
		log.Print("register url scheme: ", err)
	}
	if !mygo.App.RequestSingleInstanceLock() {
		return
	}

	conn, d, err := client.DialWithSource("gui")
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()

	a := newApp(d, "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &shell{a: a}
	a.pickerParent = func() *mygo.Window { return s.win }
	a.post = func(fn func()) {
		mygo.RunOnMain(func() {
			fn()
			s.sync()
		})
	}
	mygo.App.OnSecondInstance(func([]string, string) {
		s.show()
	})
	mygo.App.OnOpenURL(func(rawURL string) {
		s.handleDeeplink(rawURL)
	})
	mygo.App.WhenReady(func() {
		a.binary = daemonBinary()
		if v := mygo.App.Version(); v != "" { // the bundle's version, which is the app's
			a.version = v
		}
		a.cliFile = cliInstallFile()
		a.heightFile = panelHeightFile()
		a.loadPanelHeight()
		a.guiSettingsFile = guiSettingsFile()
		a.loadGUISettings()
		s.ready()
		mygo.App.OnLocaleChanged(func(string) {
			a.refreshLocalizedContent()
			if s.tray != nil && runtime.GOOS == "linux" {
				s.tray.SetMenu(s.trayMenu())
			}
			s.sync()
		})
		a.refreshLoginState()
		a.autoInstallCLI()
		a.detectPackageInstall()
		go a.watch(ctx)
		go a.watchActiveLatency(ctx)
		// The panel is the app's greeting, unless the session started it: a login item
		// stays in the tray until the user asks for it. Without a tray icon, the window is
		// the only way in, so show it always.
		if !mygo.App.WasOpenedAtLogin() || s.tray == nil {
			time.AfterFunc(400*time.Millisecond, func() {
				mygo.RunOnMain(func() {
					if !s.open {
						s.show()
					}
				})
			})
		}
		// MASIR_DEMO=servers|rules|settings|add opens that part of the panel a moment
		// after launch, for watching the transitions when no one is there to click.
		// MASIR_DEMO=cycle goes between the main page and settings, on repeat, to watch
		// the transition and its window resizing hold up.
		if demo := os.Getenv("MASIR_DEMO"); demo != "" {
			time.AfterFunc(2500*time.Millisecond, func() {
				mygo.RunOnMain(func() {
					if !s.open {
						s.show()
					}
					if demo == "cycle" {
						go demoCycle(s)
						return
					}
					switch demo {
					case "servers":
						a.open = sectionServers
					case "rules":
						a.open = sectionRules
					case "settings":
						a.openPage(settingsPath)
					case "add":
						a.openPage(addSubscriptionPath)
					}
					s.sync()
				})
			})
		}
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}
