package main

import (
	"testing"
	"time"

	pb "github.com/Gozargah/Masir/libs/masir-proto/gen/masir/daemon/v1"
	"github.com/egoist/mygo/ui"
)

// A status change may be the last event on an idle panel. The resulting
// layout must request its height without another user action or animation.
func TestMainWarningRequestsItsDrawnHeight(t *testing.T) {
	warnings := []struct {
		name string
		set  func(*app, bool)
	}{
		{"removed-server", func(a *app, on bool) { a.status.ActiveStale = on }},
		{"expired", func(a *app, on bool) {
			a.subscriptions.subscriptions[0].Usage = nil
			if on {
				a.subscriptions.subscriptions[0].Usage = &pb.Usage{ExpireAtMs: 1}
			}
		}},
		{"traffic-exhausted", func(a *app, on bool) {
			a.subscriptions.subscriptions[0].Usage = nil
			if on {
				a.subscriptions.subscriptions[0].Usage = &pb.Usage{DownloadBytes: 100, TotalBytes: 100}
			}
		}},
		{"connection-error", func(a *app, on bool) {
			a.status.LastError = ""
			if on {
				a.status.LastError = "failed to start Xray: listen tcp 127.0.0.1:7890: bind: address already in use"
			}
		}},
	}
	for _, dark := range []bool{false, true} {
		name := "light"
		if dark {
			name = "dark"
		}
		for _, warning := range warnings {
			t.Run(name+"/"+warning.name, func(t *testing.T) {
				a := newTestApp(ukServers())
				tt := ui.NewTester(a.view, panelWidth, 600)
				tt.SetDark(dark)
				tt.Frame()
				tt.Advance(time.Second)
				base := a.mainHeight
				requested := base
				calls := 0
				a.askedHeight = base
				a.onHeight = func(h float32) { requested = h; calls++ }
				warning.set(a, true)
				tt.Frame()
				tt.Advance(time.Second)
				// Find reads the current layout, unlike Element.Bounds during a build.
				footer, ok := tt.Find("Masir Fork v0.1.0")
				if !ok {
					t.Fatal("footer not found")
				}
				if requested < footer.Y+footer.H || requested < base+25 {
					t.Fatalf("warning clipped: requested %.1f, footer bottom %.1f, base %.1f", requested, footer.Y+footer.H, base)
				}
				tt.SetSize(panelWidth, int(requested+0.5))
				before := calls
				tt.Advance(time.Second)
				if tt.FramePending() || calls != before {
					t.Fatal("idle warning keeps requesting frames or resizes")
				}
				warning.set(a, false)
				tt.Frame()
				tt.Advance(time.Second)
				if abs32(requested-base) > 0.5 {
					t.Fatalf("warning removal requested %.1f, want %.1f", requested, base)
				}
			})
		}
	}
}
