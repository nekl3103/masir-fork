package main

import (
	"github.com/egoist/mygo/ui"
	"testing"
)

func TestCenteredPageHeadersHaveBalancedVerticalSpacing(t *testing.T) {
	for _, tc := range []struct {
		name           string
		path           pagePath
		title, content string
		inset          float32
	}{
		{"subscriptions", subscriptionsPath, "Subscriptions", "Open marzban", 1},
		{"details", subscriptionDetailsPath, "Subscription", "marzban", 0},
		{"edit", editSubscriptionPath, "Edit Subscription", "NAME", 1 + 10},
		{"add", addSubscriptionPath, "Add Subscription", "LABEL (OPTIONAL)", 1 + tw(2.5)},
		{"settings", settingsPath, "Settings", "LANGUAGE", 1 + tw(2.5)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := newTestApp(ukServers())
			a.subscriptionPage.id = "s1"
			pg := pageOf(tc.path)
			tt := ui.NewTester(func(c *ui.Context) { pg.build(a, c, paletteFor(c)) }, int(panelWidth), int(pg.height))
			tt.Frame()
			title, ok := tt.Find(tc.title)
			if !ok {
				t.Fatal("missing page title")
			}
			content, ok := tt.Find(tc.content)
			if !ok {
				t.Fatal("missing page content")
			}
			above := title.Y
			below := content.Y - tc.inset - (title.Y + title.H)
			if abs32(above-below) > 1 {
				t.Fatalf("unbalanced title spacing: above=%v below=%v", above, below)
			}
		})
	}
}

func TestSubscriptionListKeepsScrollbarOutsideCards(t *testing.T) {
	a := newTestApp(ukServers())
	tt := ui.NewTester(func(c *ui.Context) { a.subscriptionsView(c, paletteFor(c)) }, int(panelWidth), 520)
	tt.Frame()
	list, ok := tt.Find("Subscription list")
	if !ok {
		t.Fatal("missing list")
	}
	card, ok := tt.Find("Open marzban")
	if !ok {
		t.Fatal("missing card")
	}
	if gap := list.X + list.W - card.X - card.W; gap < pageInset-1 {
		t.Fatalf("no scrollbar gutter beside cards: %v", gap)
	}
}
