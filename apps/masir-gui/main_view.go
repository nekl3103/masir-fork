package main

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	pb "github.com/Gozargah/Masir/libs/masir-proto/gen/masir/daemon/v1"
)

func (a *app) mainView(c *ui.Context, p palette) {
	layer := ui.Box(c.Key("main-layer")).Absolute().Top(0).Left(0).Width(panelWidth)
	layer.Children(func() {
		col := ui.Column(c.Key("main-col")).Padding(12, 12, 10).Gap(tw(2.5)).Shrink(0)
		// Bounds below belongs to the previous layout. If this frame changes
		// the content height, rebuild once to measure it and let settleHeight
		// ask the shell to resize. A static warning must not depend on another
		// animation or status event arriving to get that measurement.
		col.Draw(func(_ *ui.Painter, r ui.Rect) {
			if pageOf(a.path).main && abs32(r.H-a.mainHeight) > 0.5 {
				c.Invalidate()
			}
		})
		col.Children(func() {
			a.header(c, p)
			a.banner(c, p)
			if a.reachable && len(a.subscriptions.servers) == 0 {
				a.emptyState(c, p)
			} else {
				a.modeSection(c, p)
				a.routingCard(c, p)
			}
			a.footer(c, p)
		})
		if h := col.Bounds().H; h > 0 {
			a.mainHeight, a.measuredOpen = h, a.open
			if a.motion.servers.x == 0 && a.motion.rules.x == 0 {
				a.baseHeight = h
				a.rememberPanelHeight(h) // for the first open of the next launch
			}
		}
	})
	if pageOf(a.path).main {
		a.settleHeight()
	}
}

// ---- header ----

func (a *app) statusText() (title, subtitle string, color ui.Color, pulse bool) {
	return a.statusTextAt(time.Now())
}

func (a *app) statusTextAt(now time.Time) (title, subtitle string, color ui.Color, pulse bool) {
	p := a.palette
	switch {
	case !a.reachable && a.status.GetServiceInstalled() && !a.status.GetServiceRunning():
		// A stopped service is the supervisor: nothing will answer until it is started,
		// and the connect switch is the way to start it.
		if a.busy != "" {
			return a.busy, "Starting the background service", p.degraded, true
		}
		return "Disconnected", "The background service is stopped", p.neutral, false
	case !a.reachable:
		return "Starting…", "Waiting for the masir daemon", p.degraded, true
	case a.busy != "":
		return a.busy, "Starting core...", p.degraded, true
	case a.connected():
		sub := sessionClock(now.Sub(a.since))
		if a.since.IsZero() {
			sub = sessionClock(0)
		}
		if a.connection.testing {
			return "Connected", "Checking connection…", p.healthy, false
		}
		if a.connection.checked && a.connection.identity == a.connectionIdentity() {
			_, healthy := a.connectionMessage()
			if now.Before(a.connection.resultUntil) {
				sub = a.connectionResult()
			}
			if !healthy {
				return "Connection issue", sub, p.degraded, false
			}
		}
		return "Connected", sub, p.healthy, false
	case a.wantsOn():
		return "Connecting...", "Starting core...", p.degraded, true
	}
	return "Disconnected", "Ready to connect", p.neutral, false
}

func (a *app) header(c *ui.Context, p palette) {
	now := c.Now()
	title, sub, color, pulse := a.statusTextAt(now)
	if a.connected() && !a.since.IsZero() {
		// The frame scheduler wakes only for the next clock tick or result expiry.
		c.After(time.Second - max(0, now.Sub(a.since))%time.Second)
	}
	if a.connected() {
		if remaining := a.connection.resultUntil.Sub(now); remaining > 0 {
			c.After(remaining)
		}
	}
	connecting := pulse
	ui.Row(c).Padding(0, tw(0.5), tw(0.5)).Gap(tw(2)).AlignItems(ui.Center).Justify(ui.SpaceBetween).Children(func() {
		detail, _ := a.connectionMessage()
		status := ui.ButtonBase(c.Key("test-connection")).Label("Test connection").Tooltip(detail + "\nClick to check connection").Cursor(ui.CursorPointer).Disabled(!a.reachable || a.busy != "").AlignItems(ui.Start).Gap(tw(2)).Grow(1).Basis(0).MinWidth(0)
		status.Children(func() {
			dot := ui.Box(c).Size(tw(2.5), tw(2.5)).Radius(tw(1.25)).Margin(tw(1.5), 0, 0).Shrink(0)
			statusDot(c, dot, color, connecting, a.connected(), p)
			ui.Column(c).MinHeight(31).Justify(ui.Center).Grow(1).Basis(0).MinWidth(0).Children(func() {
				label(c, title, sizeTitle, 17, 600, p.text).TextAlign(ui.Start).SingleLine()
				subColor := p.muted
				if a.connected() && p.dark {
					subColor = p.text.Alpha(0.65)
				}
				if connecting {
					subColor = p.degraded
				}
				w := 400
				if connecting {
					w = 500
				}
				label(c, sub, sizeCaption, 14, w, subColor).FontFeatures("tnum").SingleLine()
			})
		})
		if status.Clicked() && a.reachable && a.busy == "" {
			a.testConnection()
		}
		ui.Row(c).AlignItems(ui.Center).Gap(tw(2.5)).Shrink(0).Children(func() {
			on := a.wantsOn() && a.connected() || a.busy == "Connecting…"
			// With the daemon down the server list is unknown, but connect can bring it
			// up (starting a stopped service, or a root daemon when none is installed);
			// only an empty list while the daemon answers keeps the switch off.
			disabled := a.busy != "" || a.serviceBusy || (a.reachable && len(a.subscriptions.servers) == 0)
			if next, changed := nativeSwitch(c, "power", "Connect", on, switchDefault, disabled, a.busy != "", p); changed {
				a.setPower(next)
			}
			a.subscriptionsButton(c, p)
			if iconButton(c, "add", iconPlus, "Add Subscription", p, iconSM, variantGhost, 18) {
				a.openPage(addSubscriptionPath)
			}
		})
	})
}

// sessionClock keeps the subtitle useful even in the first minute.
func sessionClock(elapsed time.Duration) string {
	seconds := int64(max(0, elapsed) / time.Second)
	return fmt.Sprintf("%02d:%02d:%02d", seconds/3600, seconds/60%60, seconds%60)
}

// triangle turns a rising 0 to 1 into 0 to 1 to 0, for motion that goes and comes back.
func triangle(t float32) float32 { return float32(1 - math.Abs(float64(2*t-1))) }

// banner shows what the daemon says is wrong with what runs.
func (a *app) banner(c *ui.Context, p palette) {
	a.connectionWarnings(c, p)
	if a.status == nil || a.status.LastError == "" || a.status.LastError == a.dismissed {
		return
	}
	ui.Row(c).Gap(tw(1.5)).Padding(tw(2.5), tw(3)).Radius(cardRadius).Background(p.errorSurface).Border(1, p.errorBorder).AlignItems(ui.Start).Children(func() {
		ui.Column(c).Grow(1).Gap(tw(1.5)).Children(func() {
			label(c, "Connection Failed", sizeSubhead, 16, 600, p.errorC)
			mono(c, a.status.LastError, sizeCaption, 14, 400, p.text).MaxLines(4).Selectable()
		})
		if iconButton(c, "dismiss", iconX, "Dismiss", p, iconXS, variantGhost, 11) {
			a.dismissed = a.status.LastError
		}
	})
}

func (a *app) emptyState(c *ui.Context, p palette) {
	ui.Column(c).AlignItems(ui.Center).Gap(tw(3.5)).Padding(tw(6)).Radius(cardRadius).Background(p.card).Border(1, p.borderDefault).Children(func() {
		ui.Box(c).Size(tw(12), tw(12)).Radius(tw(4)).Background(p.accentSurface).Center().Children(func() {
			ui.Icon(c, iconLayers).FontSize(24).TextColor(p.accent)
		})
		label(c, "No Subscriptions Added", sizeTitle, 17, 600, p.text)
		label(c, "Add a subscription URL to import servers and configure routing rules.", sizeCaption, 15, 400, p.muted).TextAlign(ui.Center)
		btn := ui.ButtonBase(c.Key("empty-add")).Height(36).Padding(0, tw(3.5)).Radius(tw(2)).Background(p.accent).Gap(tw(1.5)).Center().Cursor(ui.CursorPointer).Margin(tw(1), 0, 0)
		btn.Children(func() {
			ui.Icon(c, iconPlus).FontSize(15).TextColor(ui.Hex("#ffffff"))
			label(c, "Add Subscription", sizeBody, 18, 500, ui.Hex("#ffffff"))
		})
		if btn.Clicked() {
			a.openPage(addSubscriptionPath)
		}
	})
}

// ---- mode ----

func (a *app) modeSection(c *ui.Context, p palette) {
	idx := 0
	if a.fallback() == pb.Fallback_FALLBACK_DIRECT {
		idx = 1
	}
	ui.Column(c).Gap(tw(1.5)).Children(func() {
		if next, changed := segmented(c, "mode", []string{"Tunnel", "Direct"}, idx, p); changed {
			if next == 0 {
				a.setFallback(pb.Fallback_FALLBACK_PROXY)
			} else {
				a.setFallback(pb.Fallback_FALLBACK_DIRECT)
			}
		}
		text := "Traffic uses the tunnel unless a rule matches."
		if idx == 1 {
			text = "Traffic goes direct unless a rule matches."
		}
		ui.Row(c).AlignItems(ui.Center).Gap(4).PaddingEnd(tw(1)).Children(func() {
			label(c, text, sizeCaption, 15, 400, p.muted).SingleLine().Grow(1).Basis(0).MinWidth(0)
		})
	})
}

func (a *app) subscriptionsButton(c *ui.Context, p palette) {
	if iconButton(c, "manage-subscriptions", iconLayers, "Subscriptions", p, iconSM, variantGhost, 15) {
		a.subscriptionPage = subscriptionPageState{}
		a.openPage(subscriptionsPath)
	}
}

// ---- routing card ----

func (a *app) routingCard(c *ui.Context, p palette) {
	card := ui.Column(c).Radius(cardRadius).Background(p.card).Border(1, p.borderDefault).Clip().Shrink(0)
	if p.dark {
		card.Shadow(0, 1, 4, 0, ui.RGBA(0, 0, 0, 0.2))
	} else {
		card.Shadow(0, 1, 3, 0, ui.RGBA(0, 0, 0, 0.05))
	}
	card.Children(func() {
		a.serverRow(c, p)
		a.accordion(c, "servers", a.motion.servers.x, a.open == sectionServers, func() { a.serverPicker(c, p) })
		ui.Box(c).Height(0.7).Background(p.borderSubtle).Shrink(0)
		a.rulesHeader(c, p)
		a.accordion(c, "rules", a.motion.rules.x, a.open == sectionRules, func() { a.rulesPanel(c, p) })
	})
}

func (a *app) onSectionClosed(s section) {
	switch s {
	case sectionServers:
		a.serverScrollRestored = false
		a.lastServerTop = -1
		a.lastServerQuery = ""
		a.flushGUISettings()
	case sectionRules:
		a.ruleScrollRestored = false
		a.lastRuleTop = -1
		a.lastRuleEnd = -1
		a.lastRuleQuery = ""
		a.flushGUISettings()
	}
}

func (a *app) toggle(s section) {
	if a.open == s {
		a.onSectionClosed(s)
		a.open = sectionNone
		a.clearSearch(s)
	} else {
		if a.open != sectionNone {
			a.onSectionClosed(a.open)
		}
		a.clearSearch(a.open)
		a.open = s
	}
	// The window takes the list's height as it unfolds, and gives it back as it folds: it
	// is one resize with the list's movement, not one after it.
	a.requestHeight()
}

// clearSearch empties the search of a list when it closes, so it opens on the whole list
// next time.
func (a *app) clearSearch(s section) {
	switch s {
	case sectionServers:
		a.query = ""
	case sectionRules:
		a.ruleQuery = ""
	}
}

// panelBar is the search row at the top of the open panels: the field and a button.
func panelBar(c *ui.Context, p palette, field func(), button func()) {
	ui.Column(c).Children(func() {
		ui.Box(c).Height(1).Background(p.borderSubtle).Shrink(0)
		ui.Row(c).Padding(tw(2)).Gap(tw(1)).AlignItems(ui.Center).Shrink(0).Children(func() {
			field()
			button()
		})
		ui.Box(c).Height(1).Background(p.borderSubtle).Shrink(0)
	})
}

func (a *app) footer(c *ui.Context, p palette) {
	ui.Column(c).Shrink(0).Children(func() {
		ui.Row(c).Padding(0, tw(0.5), 0).MinHeight(tw(6)).AlignItems(ui.Center).Justify(ui.SpaceBetween).Children(func() {
			if a.showUpdateControl() {
				ui.Row(c).Gap(tw(1)).AlignItems(ui.Center).Children(func() {
					a.updateControl(c, p, false)
				})
			} else {
				label(c, "Masir Fork "+versionLabel(a.version), sizeCaption, 15, 400, p.muted).LetterSpacing(0.275)
			}
			ui.Row(c).Gap(tw(2)).AlignItems(ui.Center).Children(func() {
				ui.Row(c).Gap(tw(2)).AlignItems(ui.Center).Children(func() {
					a.speedLabel(c, p, "down", a.down, "↓")
					label(c, "•", sizeMicro, 14, 400, p.quaternary)
					a.speedLabel(c, p, "up", a.up, "↑")
				})
				if iconButton(c, "settings", iconSettings, "Settings", p, iconXS, variantGhost, 14) {
					a.openPage(settingsPath)
				}
			})
		})
	})
}

func versionLabel(v string) string {
	if v == "" || v == "dev" {
		return "dev"
	}
	if !strings.HasPrefix(v, "v") {
		return "v" + v
	}
	return v
}

// speedLabel shows a speed as the footer does: the number rolls, the unit and arrow stay.
func (a *app) speedLabel(c *ui.Context, p palette, key string, bps uint64, arrow string) {
	num, unit := speedParts(bps)
	ui.Row(c).AlignItems(ui.Center).Shrink(0).Children(func() {
		rollingNumber(c, key, float64(bps), num, sizeMicro, 14, 400, p.muted, true)
		mono(c, unit+arrow, sizeMicro, 14, 400, p.muted)
	})
}
