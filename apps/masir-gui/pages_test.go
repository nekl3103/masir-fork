package main

import (
	"slices"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// TestPageRegistryIsSound checks the table in page.go, which the router, the heights and
// the transition all read. It is the test that catches a page added without its row, or a
// row that cannot be drawn or sized.
func TestPageRegistryIsSound(t *testing.T) {
	if len(pages) == 0 {
		t.Fatal("the page table is empty")
	}
	// pageOf falls back to the first row, so the main page must be it.
	if !pages[0].main {
		t.Fatalf("pages[0] is %q, not the main page", pages[0].path)
	}

	seen := map[pagePath]bool{}
	mainPages := 0
	for i, pg := range pages {
		if seen[pg.path] {
			t.Fatalf("page %d repeats the path %q", i, pg.path)
		}
		seen[pg.path] = true

		if got, ok := pageAt(pg.path); !ok || got.path != pg.path {
			t.Fatalf("pageAt(%q) did not find its own row", pg.path)
		}
		if pg.title == "" {
			t.Fatalf("page %d (%s) has no title", i, pg.path)
		}
		if pg.build == nil {
			t.Fatalf("page %d (%s) has no build", i, pg.path)
		}

		if pg.main {
			mainPages++
			if pg.height != 0 {
				t.Fatalf("the main page %q has a height of %v; it measures itself", pg.path, pg.height)
			}
			continue
		}
		if pg.height <= 0 {
			t.Fatalf("the page %q has no height, so the panel cannot open at it", pg.path)
		}
	}
	if mainPages != 1 {
		t.Fatalf("%d pages are main, want exactly one", mainPages)
	}

	// Every path the app names in code has to have a row; a new constant means a new
	// line here, which is the point: the compiler cannot check the table for itself.
	for _, p := range []pagePath{mainPath, settingsPath, addSubscriptionPath, subscriptionsPath, subscriptionDetailsPath, editSubscriptionPath} {
		if _, ok := pageAt(p); !ok {
			t.Fatalf("the page %q has no row", p)
		}
	}
}

// TestEveryPageOpensAndReturns opens every page in the registry from the main one, lets
// the transition settle, checks the window settled at that page's height, then goes Back
// and checks it settles on the main page's height again. It walks `pages` rather than a
// list of its own, so a new page is covered the moment it has a row.
func TestEveryPageOpensAndReturns(t *testing.T) {
	a := newTestApp(ukServers())
	a.motion.instant = false
	var asked []float32
	a.onHeight = func(h float32) { asked = append(asked, h) }
	tt := ui.NewTester(a.view, panelWidth, 900)
	tt.Frame()

	mainHeight := a.restHeight()
	if mainHeight <= 0 {
		t.Fatalf("the main page measured %v", mainHeight)
	}

	// settle advances frames until the page spring is aimed at want and every spring has
	// come to rest, as the window would. A spring that has not been aimed yet is
	// "settled" already, so the aim is part of the condition.
	settle := func(t *testing.T, want float32) {
		t.Helper()
		for i := 0; i < 200; i++ {
			tt.Advance(20 * time.Millisecond)
			if a.motion.page.target == want && a.motion.settled() {
				if got := a.motion.page.x; got != want {
					t.Fatalf("the page settled at %v, want %v", got, want)
				}
				return
			}
		}
		t.Fatalf("the panel did not settle at %v: page at %v", want, a.motion.page.x)
	}

	for _, pg := range pages {
		if pg.main {
			continue
		}
		t.Run(string(pg.path), func(t *testing.T) {
			asked = nil
			a.openPage(pg.path)
			settle(t, 1)

			if a.path != pg.path {
				t.Fatalf("path = %q, want %q", a.path, pg.path)
			}
			if a.lastSub != pg.path {
				t.Fatalf("lastSub = %q, want %q, so the window can shrink from it", a.lastSub, pg.path)
			}
			wantHeight := pg.height
			switch pg.path {
			case addSubscriptionPath:
				wantHeight = a.importHeight
				if wantHeight < pg.height || wantHeight > pageOf(subscriptionDetailsPath).height {
					t.Fatalf("invalid measured import height %v", wantHeight)
				}
			case ruleEditPath:
				wantHeight = a.rulePage.editHeight
				if wantHeight <= 0 || wantHeight > pg.height {
					t.Fatalf("invalid measured rule height %v", wantHeight)
				}
			}
			if got := a.restHeight(); got != wantHeight {
				t.Fatalf("restHeight = %v, want the page's %v", got, pg.height)
			}
			if !slices.Contains(asked, wantHeight) {
				t.Fatalf("opening asked for %v, want the page's %v among them", asked, pg.height)
			}

			asked = nil
			a.goBack()
			settle(t, 0)

			if a.path != mainPath {
				t.Fatalf("after Back, path = %q, want %q", a.path, mainPath)
			}
			if got := a.restHeight(); got != mainHeight {
				t.Fatalf("after Back, restHeight = %v, want the main page's %v", got, mainHeight)
			}
			if !slices.Contains(asked, mainHeight) {
				t.Fatalf("Back asked for %v, want the main page's %v among them", asked, mainHeight)
			}
		})
	}
}
