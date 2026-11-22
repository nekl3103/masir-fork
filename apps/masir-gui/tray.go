package main

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"runtime"

	"github.com/egoist/mygo"

	pb "github.com/Gozargah/Masir/libs/masir-proto/gen/masir/daemon/v1"
)

func (s *shell) trayMenu() *mygo.Menu {
	return mygo.NewMenu([]*mygo.MenuItem{
		{Label: tr("Open Masir"), Click: func(*mygo.MenuItem, *mygo.Window) { s.show() }},
		mygo.Separator(),
		{Label: tr("Quit Masir"), Role: mygo.RoleQuit},
	})
}

// updateTray shows the state in the menu bar: the logo is green while masir is on.
func (a *app) updateTray(t *mygo.Tray) {
	if t == nil {
		return
	}
	on := a.connected()
	if on != a.trayOn {
		a.trayOn = on
		if icon, err := trayIcon(on); err == nil {
			// Off: black, which the menu bar tints. On: green, which it must leave as it is.
			_ = t.SetIcon(icon, runtime.GOOS == "darwin" && !on)
		}
	}
	t.SetToolTip(a.tooltip())
}

func (a *app) tooltip() string {
	mode := tr("Tunnel")
	if a.fallback() == pb.Fallback_FALLBACK_DIRECT {
		mode = tr("Direct")
	}
	if a.connected() {
		s := fmt.Sprintf(tr("Masir: Connected · %s"), mode)
		if srv, ok := a.active(); ok {
			_, name, _ := serverTitle(srv.cfg.GetName())
			s += " (" + name + ")"
		}
		return s
	}
	return tr("Masir: Disconnected · ") + mode
}

// trayGreen is the logo while masir is on, the same green as the status dot.
var trayGreen = color.NRGBA{R: 0, G: 214, B: 136, A: 255}

// trayBlue is the logo while masir is off outside the macOS menu bar: the accent color,
// which shows on the light and the dark taskbars alike.
var trayBlue = color.NRGBA{R: 10, G: 132, B: 255, A: 255}

// trayIcon draws the Masir logo: 45° angled parallel lines with rounded caps.
// Off: black on transparent for macOS to tint, and accent blue on other systems.
// On: green, matching the status dot.
func trayIcon(on bool) ([]byte, error) {
	const size, ss = 32, 4 // pixels, and samples per pixel side
	// Follow the white mark in resources/icon.png, with slightly heavier strokes
	// for menu-bar legibility. Give the outer strokes a little extra separation
	// while preserving the compact silhouette and longer middle diagonal.
	const halfW = 4.5 / 2

	lines := [3][2][2]float64{
		{{3.7, 20.0}, {13.4, 10.3}},
		{{7.9, 24.1}, {24.1, 7.9}},
		{{18.6, 21.7}, {28.3, 12.0}},
	}

	distToLines := func(x, y float64) float64 {
		best := math.MaxFloat64
		for _, l := range lines {
			x1, y1 := l[0][0], l[0][1]
			x2, y2 := l[1][0], l[1][1]
			dx, dy := x2-x1, y2-y1
			l2 := dx*dx + dy*dy
			t := math.Max(0, math.Min(1, ((x-x1)*dx+(y-y1)*dy)/l2))
			d := math.Hypot(x-(x1+t*dx), y-(y1+t*dy))
			if d < best {
				best = d
			}
		}
		return best
	}

	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	for py := 0; py < size; py++ {
		for px := 0; px < size; px++ {
			hits := 0
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					x := float64(px) + (float64(sx)+0.5)/ss
					y := float64(py) + (float64(sy)+0.5)/ss
					if distToLines(x, y) <= halfW {
						hits++
					}
				}
			}
			if hits > 0 {
				ink := color.NRGBA{A: 255}
				switch {
				case on:
					ink = trayGreen
				case runtime.GOOS != "darwin":
					ink = trayBlue
				}
				ink.A = uint8(255 * hits / (ss * ss))
				img.SetNRGBA(px, py, ink)
			}
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}
