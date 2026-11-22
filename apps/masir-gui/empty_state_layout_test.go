package main

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// Empty-state content and incoming server data must resize an idle panel without
// clipping its footer or leaving a resize loop running.
func TestEmptyStateHeightFollowsContent(t *testing.T) {
	for _, dark := range []bool{false, true} {
		name := "light"
		if dark {
			name = "dark"
		}
		t.Run(name, func(t *testing.T) {
			f := ukServers()
			a := newTestApp(f)
			requested, calls := float32(0), 0
			a.onHeight = func(h float32) { requested = h; calls++ }
			tt := ui.NewTester(a.view, panelWidth, 600)
			tt.SetDark(dark)
			checkHeight := func() {
				t.Helper()
				tt.Frame()
				tt.Advance(time.Second)
				footer, ok := tt.Find("Masir Fork v0.1.0")
				if !ok || requested < footer.Y+footer.H || abs32(requested-a.mainHeight) > 0.5 {
					t.Fatalf("height does not fit content: requested %.1f, content %.1f, footer %v", requested, a.mainHeight, footer)
				}
				tt.SetSize(panelWidth, int(requested+0.5))
				before := calls
				tt.Advance(time.Second)
				// Startup deliberately pulses its status dot, but must not resize.
				if (a.reachable && tt.FramePending()) || calls != before {
					t.Fatal("settled content keeps requesting frames or resizes")
				}
			}
			checkHeight()
			selectedHeight := requested
			a.status.ActiveSubscriptionId, a.status.ActiveConfigId, a.status.ActiveConfigName = "", "", ""
			checkHeight()
			if !tt.HasText("Choose a server to connect") || abs32(requested-selectedHeight) > 0.5 {
				t.Fatal("unselected server must keep the selected row's two-line height")
			}
			if err := tt.Click(ltr("Select a server")); err != nil {
				t.Fatal(err)
			}
			if a.open != sectionServers {
				t.Fatal("server prompt did not open the picker")
			}
			a.toggle(sectionServers)
			a.subscriptions.subscriptions, a.subscriptions.servers = nil, nil
			checkHeight()
			if !tt.HasText("No Subscriptions Added") {
				t.Fatal("missing subscription empty state")
			}
			emptyHeight := requested
			if err := tt.Click("Add Subscription"); err != nil {
				t.Fatal(err)
			}
			tt.Advance(time.Second)
			a.openPage(mainPath)
			checkHeight()
			if abs32(requested-emptyHeight) > 0.5 {
				t.Fatal("returning from import did not restore the empty state's content height")
			}
			a.reachable = false
			checkHeight()
			if !tt.HasText("Waiting for available servers") {
				t.Fatal("starting daemon has no server guidance")
			}
		})
	}
}
