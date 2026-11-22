package main

import (
	"runtime"

	"github.com/egoist/mygo/ui"
)

// view is the panel's content: the page the person is on, over the system's material.
func (a *app) view(c *ui.Context) {
	p := paletteFor(c)
	a.palette = p
	a.tuneTheme(c, p)
	c.Root().Background(ui.Transparent)

	for _, msg := range a.notices {
		c.AddToast(ui.Toast{ID: msg, Title: msg, Type: a.noticeTypes[msg], Timeout: noticeTimeout(a.noticeTypes[msg])})
	}
	a.notices = nil
	clear(a.noticeTypes)

	// The native popover owns both its body and arrow. Keep the content
	// transparent so a tint on the material covers the whole shape.
	root := ui.Box(c).Fill().TextColor(p.text).FontSize(sizeBody).FixedLineHeight(18).Clip()
	if !c.Vibrancy() {
		root.Background(c.Theme().Surface)
		if runtime.GOOS == "darwin" {
			root.Radius(panelRadius).Border(panelBorderWidth(c), p.borderDefault)
		}
	}
	root.Children(func() {
		a.syncPage()
		a.aimMotion(c)

		// The settings page lays out about a hundred labels and draws in about
		// fifty batches the first time it is built, which is the one frame of
		// its slide that costs more than a millisecond. Build and paint it
		// once, all but invisible and inert, as the panel opens: the next
		// frame drops it, so it is never seen or read.
		if !a.noWarm && !a.warmed[settingsPath] && a.router.Path() != string(settingsPath) {
			a.warmed[settingsPath] = true
			// On screen but all but invisible: off screen the paint is culled,
			// and its GPU pipelines would still be built on the frame that
			// first draws the page.
			warm := ui.Box(c).Absolute().Top(0).Left(0).Width(panelWidth).
				Height(pageOf(settingsPath).height).Opacity(0.002).Role(ui.RoleNone)
			warm.Children(func() {
				pageOf(settingsPath).build(a, c, p)
			})
			c.AnimationFrame() // the next frame leaves it out
		}

		a.router.View(c, func(r *ui.Route) {
			if pg, ok := pageAt(pagePath(r.Path())); ok {
				r.Title(tr(pg.title))
				pg.build(a, c, p)
			}
		})
		a.contextMenu(c, p)
	})
	a.toastViewport(c, p)
}

// panelBorderWidth is the space the fallback surface takes around its pages.
// Native vibrancy supplies the panel frame itself.
func panelBorderWidth(c *ui.Context) float32 {
	if !c.Vibrancy() && runtime.GOOS == "darwin" {
		return 1
	}
	return 0
}
