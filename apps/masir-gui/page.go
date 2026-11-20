package main

import "github.com/egoist/mygo/ui"

// pagePath is where a page lives in the router, and its key in the table below.
type pagePath string

const (
	ruleDetailsPath         pagePath = "/rules/details"
	ruleEditPath            pagePath = "/rules/edit"
	routeCheckPath          pagePath = "/rules/check-domain"
	subscriptionsPath       pagePath = "/subscriptions"
	subscriptionDetailsPath pagePath = "/subscriptions/details"
	mainPath                pagePath = "/"
	settingsPath            pagePath = "/settings"
	addSubscriptionPath     pagePath = "/subscriptions/add"
	editSubscriptionPath    pagePath = "/subscriptions/details/edit"
)

// page is one page of the panel: where the router shows it, how tall it opens
// over the main page, what it does as it opens, and how it draws itself. A new
// page is one row here and its build method; openPage, syncPage and the
// router's view all read the same table.
type page struct {
	path   pagePath
	title  string
	height float32 // sub pages: how tall they must be
	enter  func(*app)
	build  func(*app, *ui.Context, palette)
	main   bool
}

var pages []page

func init() {
	// In init, not a package-level initializer: the page builders reach
	// the height code, which reads this table back.
	pages = []page{
		{
			path:   mainPath,
			title:  "Masir Fork",
			height: 0,
			enter:  nil,
			build:  (*app).mainView,
			main:   true,
		},
		{path: ruleDetailsPath, title: "Rule", height: 560, build: (*app).ruleDetailsView},
		{path: ruleEditPath, title: "Edit Rule", height: 620, build: (*app).ruleEditView},
		{path: routeCheckPath, title: "Check Domain Route", height: 365, build: (*app).routeCheckView},
		{path: subscriptionsPath, title: "Subscriptions", height: 520, build: (*app).subscriptionsView},
		{path: subscriptionDetailsPath, title: "Subscription", height: 560, build: (*app).subscriptionDetailsView},
		{path: editSubscriptionPath, title: "Edit Subscription", height: subscriptionEditHeight, build: (*app).subscriptionEditView},
		{
			path:   settingsPath,
			title:  "Settings",
			height: settingsHeight,
			enter:  (*app).enterSettings,
			build:  (*app).settingsView,
			main:   false,
		},
		{
			path:   addSubscriptionPath,
			title:  "Add Subscription",
			height: addSubscriptionHeight,
			enter:  nil,
			build:  (*app).addSubscriptionView,
			main:   false,
		},
	}
}

// returns the page at a path.
func pageOf(s pagePath) page {
	for _, pg := range pages {
		if pg.path == s {
			return pg
		}
	}
	return pages[0]
}

// pageAt returns the page the router shows at path.
func pageAt(path pagePath) (page, bool) {
	for _, pg := range pages {
		if pg.path == path {
			return pg, true
		}
	}
	return page{}, false
}

// installRouter gives a router the panel's page behavior. The app places the
// two pages of a change itself, with the panel's own page spring, so the router
// only needs to keep them both built for as long as that spring takes. A router
// is made again after the popover closes, so this belongs here.
func (a *app) installRouter() {
	a.router = ui.NewRouter("/")
	a.router.Animation = &ui.Animation{Duration: pageMotionDuration, Ease: easeApp}
	setPageTransition(a)
	// The router starts on the main page, and so does the panel. The field's
	// zero value is no page at all, which every "is this a sub page" test would
	// read as one; a router is made before the first frame, so it lands here.
	a.path, a.lastSub = mainPath, mainPath
	a.subpageMove = false
	a.motion.detail = spring{}
}

// resetRouter puts the router back at the main page with an empty history:
// closing the panel is not a page to come back to, so Back has nowhere to go
// when it opens again.
func (a *app) resetRouter() {
	if a.router.Path() == string(mainPath) && !a.router.CanGoBack() && !a.router.CanGoForward() {
		return
	}
	a.installRouter()
}

// openPage goes to a page through the router. A page opening from the main one asks for
// its height in the click's frame; a window that follows the springs takes it a frame at a
// time instead, from the page syncPage reads.
func (a *app) openPage(s pagePath) {
	a.router.Push(string(pageOf(s).path))
	a.syncPage()
	a.requestHeight()
}

// syncPage takes the page shown from the router, so the height that follows it knows
// which page is on screen after a push, Back, or the router's own history keys. Entering a
// page refreshes what it shows.
func (a *app) syncPage() {
	p := pagePath(a.router.Path())
	pg, ok := pageAt(p)
	if !ok {
		pg = pages[0]
	}
	if a.path == pg.path {
		return
	}
	a.fromHeight = a.windowHeight()
	a.subpageMove = !pageOf(a.path).main && !pg.main
	a.motion.detail = spring{}
	if a.subpageMove {
		a.motion.detail.target = 1
		if a.motion.instant {
			a.motion.detail.x = 1
		}
	}
	a.path = pg.path
	if !pg.main {
		a.lastSub = pg.path
	}
	a.toHeight = a.restHeight()
	if !a.motion.instant {
		a.holdPage = true // build the pages at rest, then let the spring go
	}
	if pg.enter != nil {
		pg.enter(a)
	}
}

// goBack returns through page history and sizes the window with its page spring.
func (a *app) goBack() {
	if (a.path == ruleEditPath || a.path == ruleDetailsPath) && a.rulePage.busy {
		return
	}
	if a.path == editSubscriptionPath {
		if a.subscriptionPage.busy {
			return
		}
		a.subscriptionPage.err = ""
	}
	if a.path == ruleEditPath {
		a.rulePage.removing = false
	}
	a.router.Back()
	a.syncPage()
	a.requestHeight()
}

// pageTransition places the two pages of a router change the way the two
// hand-placed layers did: the page going out and the one coming in move a full
// panel width in step, each fading with the move. The position comes from the
// panel's page spring, which aimMotion steps from the frame clock, so a slow
// frame slows the change instead of skipping part of it, and the window height
// follows the same spring.
func (a *app) pageTransition(in, out ui.Element, progress float32, dir int) {
	p := a.motion.page.x
	if a.subpageMove && dir != 0 {
		// Both pages are below main: this move has its own 0→1 spring rather
		// than the main-page spring, which is already resting at 1.
		progress := a.motion.detail.x
		direction := float32(dir)
		in.Absolute().Top(0).Width(panelWidth).Height(a.heightOr(a.toHeight)).Translate(direction*(1-progress)*panelWidth, 0).Opacity(progress)
		out.Absolute().Top(0).Width(panelWidth).Height(a.heightOr(a.fromHeight)).Translate(-direction*progress*panelWidth, 0).Opacity(1 - progress)
		return
	}
	if dir == 0 {
		// Pages side by side have no main to leave or return to: fade in place.
		in.Absolute().Top(0).Width(panelWidth).Height(a.heightOr(a.toHeight)).Opacity(progress)
		out.Absolute().Top(0).Width(panelWidth).Height(a.heightOr(a.fromHeight)).Opacity(1 - progress)
		return
	}
	main, sub := out, in
	mainH, subH := a.fromHeight, a.toHeight
	if dir < 0 {
		main, sub = in, out
		mainH, subH = a.toHeight, a.fromHeight
	}
	main.Absolute().Top(0).Width(panelWidth).Height(a.heightOr(mainH)).
		Translate(-p*panelWidth, 0).Opacity(1 - p)
	sub.Absolute().Top(0).Width(panelWidth).Height(a.heightOr(subH)).
		Translate((1-p)*panelWidth, 0).Opacity(p)
}

// heightOr is a page height for the transition, or the window's height when the
// page has not been measured yet.
func (a *app) heightOr(h float32) float32 {
	if h > 0 {
		return h
	}
	return a.windowHeight()
}
