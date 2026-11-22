package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	pb "github.com/Gozargah/Masir/libs/masir-proto/gen/masir/daemon/v1"
	"github.com/egoist/mygo"
)

// installBlock says why MyGo's updater can't replace this app, and so what does.
type installBlock int

const (
	blockNone       installBlock = iota
	blockPackageDeb              // installed by dpkg: apt-get installs the update
	blockPackageRpm              // installed by rpm: dnf installs the update
	blockReadOnly                // a portable copy in a folder the person can't write
)

// packageFiles are a downloaded, verified package and the signed list that vouches for it.
type packageFiles struct{ pkg, sums, sig string }

// packageFetch is one download for the bundled CLI's `desktop fetch-package`.
type packageFetch struct{ version, format, dir, proxy string }

// Exit codes of `masir desktop fetch-package` and `install-package` (apps/masir/exit_code.go).
const (
	pkgExitMismatch       = 10
	pkgExitBusy           = 11
	pkgExitServicePending = 13
	pkgExitNotNewer       = 14
	pkgExitUnitPending    = 16
	pkgExitUnreachable    = 20
)

// The package flow's outside world, behind variables so tests need no dpkg, pkexec or
// network.
var (
	detectInstallBlock = platformInstallBlock
	fetchPackage       = platformFetchPackage
	helperTrusted      = platformHelperTrusted
	installPackage     = platformInstallPackage
	packageCacheDir    = defaultPackageCacheDir
)

// defaultPackageCacheDir is this app's own cache, so a smoke-test copy run under
// another MASIR_APP_NAME never touches the real app's download.
func defaultPackageCacheDir() (string, error) {
	dir, err := mygo.App.Path(mygo.PathCache)
	if err != nil {
		return "", err
	}
	if dir == "" {
		return "", errors.New(tr("no cache directory"))
	}
	return filepath.Join(dir, "updates"), nil
}

// detectPackageInstall runs once at launch. Only an app MyGo can't update looks further,
// so macOS, Windows and writable portable installs never enter the package flow.
func (a *app) detectPackageInstall() {
	a.async(func() {
		block := blockNone
		if !appUpdatesEnabled() {
			block = detectInstallBlock()
		}
		a.post(func() {
			a.updates.state.block = block
			if a.updates.state.phase == updateIdle && a.hasUpdate() {
				a.updates.state.phase = updateAvailable
			}
		})
	})
}

// packageTargetVersion is the release the daemon reports. MyGo's feed can't be asked
// from a packaged install, so the daemon's latest version, available or not, is the
// only source. MASIR_DESKTOP_RELEASE_VERSION stands in for it only together with a
// loopback MASIR_DESKTOP_RELEASE_BASE, for local tests.
func packageTargetVersion(rel *pb.ReleaseInfo) string {
	if v := os.Getenv("MASIR_DESKTOP_RELEASE_VERSION"); v != "" && loopbackURL(os.Getenv("MASIR_DESKTOP_RELEASE_BASE")) {
		return v
	}
	return rel.GetLatestVersion()
}

func loopbackURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || raw == "" {
		return false
	}
	ip := net.ParseIP(u.Hostname())
	return u.Hostname() == "localhost" || (ip != nil && ip.IsLoopback())
}

// packageUpdateAvailable reports a newer release for an app MyGo can't update.
func (a *app) packageUpdateAvailable() bool {
	if a.updates.state.block == blockNone {
		return false
	}
	v := packageTargetVersion(a.updates.state.rel)
	return v != "" && newerRelease(v, a.version) && !a.ignored(v)
}

func (a *app) packageFormat() string {
	if a.updates.state.block == blockPackageRpm {
		return "rpm"
	}
	return "deb"
}

// startPackageUpdate downloads and verifies the package before offering installation.
// Pausing keeps the partial file for the next attempt; cancelling deletes it.
func (a *app) startPackageUpdate() {
	st := &a.updates.state
	version := strings.TrimPrefix(packageTargetVersion(st.rel), "v")
	// The version names a directory: only a real, newer release gets one.
	if !newerRelease(version, a.version) || filepath.Base(version) != version {
		return
	}
	format := a.packageFormat()
	// The proxy is used only when it already runs; the update never starts it.
	proxy := ""
	if a.status.GetProxyRunning() {
		proxy = fmt.Sprintf("http://127.0.0.1:%d", a.status.GetSettings().GetProxyPort())
	}
	st.pkgSync = true
	st.pkgVersion = version
	st.parts = 1
	st.err = ""
	st.phase = updateAppDownloading
	st.progress, st.total = 0, 0
	st.appPaused = false
	st.appRun++
	run := st.appRun
	ctx, cancel := context.WithCancel(context.Background())
	st.appCancel = cancel
	// After a cancelled attempt's cleanup, which would otherwise delete this download.
	a.pkgSerial(func() func() {
		root, err := packageCacheDir()
		dir := ""
		var files packageFiles
		if err == nil {
			dir = filepath.Join(root, version)
			removeOtherVersions(root, version)
			lastAt := time.Time{}
			files, err = fetchPackage(ctx, packageFetch{version, format, dir, proxy}, func(done, total int64) {
				if time.Since(lastAt) < 100*time.Millisecond && (total == 0 || done < total) {
					return
				}
				lastAt = time.Now()
				a.post(func() {
					if run != st.appRun || (st.phase != updateAppDownloading && st.phase != updateVerifying) {
						return
					}
					st.progress, st.total = done, total
					// The CLI verifies before it exits; a partial that fails the hash is
					// downloaded again, which can be paused like the first.
					if total > 0 && done >= total {
						st.phase = updateVerifying
					} else {
						st.phase = updateAppDownloading
					}
				})
			})
		}
		return func() {
			if run != st.appRun {
				return // a pause and resume started a newer attempt
			}
			st.appCancel = nil
			st.pkgDir = dir
			switch {
			case st.appPaused:
				st.phase = updateAppPaused
			case err != nil:
				a.packageFailed(err, files, format)
			default:
				a.offerPackageInstall(version, format, files)
			}
		}
	})
}

// pkgSerial runs fn off the main thread after every earlier pkgSerial call has ended,
// then posts what fn returns. The package cache's download and cleanups go through it
// in click order: a goroutine each, unordered, let a cancelled attempt's cleanup delete
// the next attempt's download. Call it on the main thread.
func (a *app) pkgSerial(fn func() (then func())) {
	st := &a.updates.state
	prev := st.pkgLast
	done := make(chan struct{})
	st.pkgLast = done
	a.async(func() {
		if prev != nil {
			<-prev
		}
		then := fn()
		close(done) // before then: it may queue the next step
		if then != nil {
			a.post(then)
		}
	})
}

func (a *app) offerPackageInstall(version, format string, files packageFiles) {
	st := &a.updates.state
	st.phase = updateReady
	st.installReady = func() { a.installPackageFiles(version, format, files) }
}

// installPackageFiles checks the helper, then asks for the password once.
func (a *app) installPackageFiles(version, format string, files packageFiles) {
	st := &a.updates.state
	st.tunWasOn = a.status.GetTunRunning()
	a.async(func() {
		bin, err := helperTrusted()
		if err != nil {
			a.post(func() { a.packageFailed(err, files, format) })
			return
		}
		a.post(func() { st.phase = updateAuthorizing })
		err = installPackage(bin, files, os.Getuid(), func() {
			a.post(func() {
				if st.phase == updateAuthorizing {
					st.phase = updateAppInstalling
				}
			})
		})
		a.post(func() { a.packageInstalled(version, files, format, err) })
	})
}

// packageInstalled follows a finished install: the new app is on disk, and the daemon
// must run the new version before the app relaunches, as an app update does.
func (a *app) packageInstalled(version string, files packageFiles, format string, err error) {
	st := &a.updates.state
	_, code, _ := elevationKindOf(err)
	pending := err != nil && (code == pkgExitServicePending || code == pkgExitUnitPending)
	if code == pkgExitUnitPending {
		// The quit handler saves GUI settings before the new app starts.
		st.unitPending = true
	} else if err == nil {
		st.unitPending = false
	}
	if err != nil && !pending {
		a.packageFailed(err, files, format)
		return
	}
	// The download is installed: delete it, then do what follows. Relaunching first
	// could quit before the deletion ran and leave the package in the cache.
	dir := st.pkgDir
	cleanThen := func(then func()) {
		if dir == "" {
			if then != nil {
				then()
			}
			return
		}
		a.pkgSerial(func() func() { os.RemoveAll(dir); return then })
	}
	relaunch := func() { relaunchApp() }
	switch {
	case pending || a.status == nil || !a.reachable:
		// The new app offers Finish Update for a service that didn't take it.
		st.relaunching = true
		cleanThen(relaunch)
	case a.status.GetServiceInstalled() && newerRelease(version, a.status.GetVersion()):
		cleanThen(func() {
			st.relaunchAfterRestart = true
			a.followDaemonRestart(version, false) // the root command restarted it
		})
	case a.daemonIsBundled():
		cleanThen(func() {
			st.relaunchAfterRestart = true
			a.restartDaemon(version)
		})
	default:
		st.relaunching = true
		cleanThen(relaunch)
	}
}

// packageFailed shows why the package flow stopped. A dismissed dialog isn't a failure.
func (a *app) packageFailed(err error, files packageFiles, format string) {
	st := &a.updates.state
	st.pkgSync = false
	text, remove := packageFailure(err)
	if text == "" {
		st.pkgSync = true
		st.err = ""
		a.offerPackageInstall(st.pkgVersion, format, files)
		return
	}
	if remove && st.pkgDir != "" {
		dir := st.pkgDir
		a.pkgSerial(func() func() { os.RemoveAll(dir); return nil })
	}
	st.phase = updateFailed
	st.err = text
	a.notifyError(tr("The update failed: ") + text)
}

// packageFailure is the text for a failed package update, "" for a dismissed dialog,
// and whether the download must go (it didn't match the signed release).
func packageFailure(err error) (text string, deleteDownload bool) {
	kind, code, ok := elevationKindOf(err)
	if !ok {
		return describe(err), false
	}
	switch kind {
	case elevationCancelled:
		return "", false
	case elevationDenied:
		return tr("Your account isn't allowed to install software."), false
	case elevationUnavailable:
		return tr("Masir can't ask for administrator permission here. Download the new version from the release page and verify its signed checksum before installing."), false
	}
	switch code {
	case pkgExitBusy:
		return tr("Another installer is running. Try again in a minute."), false
	case pkgExitMismatch:
		return tr("The download didn't match the signed release; nothing was installed."), true
	case pkgExitUnreachable:
		return tr("GitHub isn't reachable. Connect Masir and try again."), false
	case pkgExitNotNewer:
		return tr("This version is already installed. Restart Masir."), false
	}
	msg := strings.TrimPrefix(err.Error(), "root permission wasn't granted: ")
	lines := strings.Split(strings.TrimSpace(msg), "\n")
	return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(lines[len(lines)-1]), "error: ")), false
}

// removeOtherVersions deletes downloads of other releases: a newer release makes an
// older partial or kept download useless.
func removeOtherVersions(root, version string) {
	entries, _ := os.ReadDir(root)
	for _, e := range entries {
		if e.Name() != version {
			os.RemoveAll(filepath.Join(root, e.Name()))
		}
	}
}
