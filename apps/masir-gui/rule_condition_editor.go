package main

import (
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"
)

func (a *app) ruleConditionEditor(c *ui.Context, p palette, d ruleCondition) {
	st := &a.rulePage
	switch d.key {
	case "network":
		index := 0
		switch st.values[d.key] {
		case "tcp":
			index = 1
		case "udp":
			index = 2
		}
		if ruleSegments(c, "network", &index, p, tr("Any"), "TCP", "UDP") {
			st.values[d.key] = []string{"", "tcp", "udp"}[index]
		}
		label(c, tr("Any includes both TCP and UDP traffic."), sizeMicro, 15, 400, p.muted)
	case "protocol":
		label(c, tr("Match one or more detected protocols. Requires protocol sniffing."), sizeMicro, 15, 400, p.muted)
		t := c.Theme()
		ui.Row(c).Label(tr("Application protocols")).Padding(t.Space(0.5)).Gap(t.Space(0.5)).Radius(t.Radius).Background(ruleFieldColor(p)).Border(1, t.Border).Shrink(0).Children(func() {
			for _, value := range []string{"http", "tls", "quic", "bittorrent"} {
				entries := ruleEntries(d.key, st.values[d.key])
				selected := slices.Contains(entries, value)
				title := strings.ToUpper(value)
				if value == "bittorrent" {
					title = "BitTorrent"
				}
				button := ui.ToggleBase(c.Key(value), &selected).Label(title).Disabled(st.busy)
				ruleSegmentStyle(button, selected, t).Children(func() { ui.Text(c, title).SingleLine().TextAlign(ui.Center) })
				if button.Changed() {
					if !selected {
						var next []string
						for _, v := range entries {
							if v != value {
								next = append(next, v)
							}
						}
						entries = next
					} else {
						entries = append(entries, value)
					}
					st.values[d.key] = strings.Join(entries, "\n")
				}
			}
		})
	case "port", "sourcePort":
		value := st.values[d.key]
		labeledInput(c, p, "ports", tr("Ports or ranges"), &value, "80, 443, 1000-2000", iconHash, p.muted, inputHeight, nil).AutoFocus()
		st.values[d.key] = value
		label(c, tr("Separate ports and ranges with commas."), sizeMicro, 15, 400, p.muted)
	case "process":
		a.ruleAppsEditor(c, p)
	case "domain":
		ui.Column(c).Padding(10, 10, 0).Children(func() {
			ruleSegments(c, "domain-mode", &st.domainMode, p, tr("Category"), tr("Domain"))
		})
		if st.domainMode == 0 {
			a.ruleGeoEditor(c, p, d)
		} else {
			ui.Column(c).Padding(0, 10, 10).Gap(8).Children(func() {
				label(c, tr("Domains include subdomains. Use full:example.com for an exact hostname, or regexp: for a regular expression."), sizeMicro, 15, 400, p.muted)
				a.ruleEntryEditor(c, p, d, tr("Domain or pattern"), "example.com, full:www.example.org")
			})
		}
	case "ip":
		ui.Column(c).Padding(10, 10, 0).Children(func() {
			ruleSegments(c, "ip-mode", &st.ipMode, p, "GeoIP", tr("Manual"))
		})
		if st.ipMode == 0 {
			a.ruleGeoEditor(c, p, d)
		} else {
			ui.Column(c).Padding(0, 10, 10).Gap(8).Children(func() { a.ruleIPInput(c, p, d) })
		}
	case "source":
		a.ruleIPInput(c, p, d)
	}

	if d.key != "process" && len(ruleEntries(d.key, st.values[d.key])) > 0 {
		reset := func() {
			if ui.Button(c.Key("any-"+d.key), tr("Reset to Any")).Disabled(st.busy).Clicked() {
				st.values[d.key] = ""
				st.err = ""
			}
		}
		if d.key == "domain" || d.key == "ip" {
			ui.Column(c).Padding(0, 10, 10).Children(reset)
		} else {
			reset()
		}
	}
}

// Editing manual IPs preserves independently selected GeoIP categories.
func (a *app) ruleIPInput(c *ui.Context, p palette, d ruleCondition) {
	var manual, categories []string
	for _, v := range ruleEntries(d.key, a.rulePage.values[d.key]) {
		if d.key == "ip" && strings.HasPrefix(v, "geoip:") {
			categories = append(categories, v)
		} else {
			manual = append(manual, v)
		}
	}
	value := strings.Join(manual, ", ")
	before := value
	labeledInput(c, p, d.key+"-ranges", tr("IP addresses or ranges"), &value, "1.1.1.1, 10.0.0.0/8", d.icon, p.muted, inputHeight, nil).AutoFocus()
	if value != before {
		a.rulePage.values[d.key] = strings.Join(append(categories, value), "\n")
	}
	label(c, tr("Separate addresses and CIDR ranges with commas."), sizeMicro, 15, 400, p.muted)
}

func (a *app) ruleEntryEditor(c *ui.Context, p palette, d ruleCondition, title, placeholder string) {
	st := &a.rulePage
	submitted := labeledInput(c, p, "condition-entry", title, &st.entry, placeholder, d.icon, p.muted, inputHeight, nil).AutoFocus().Submitted()
	if ui.Button(c.Key("add-entry"), tr("Add")).Disabled(st.busy || strings.TrimSpace(st.entry) == "").Clicked() || submitted {
		a.addRuleConditionEntry(d.key)
	}
	a.ruleConditionEntries(c, p, d)
}

func (a *app) addRuleConditionEntry(key string) {
	st := &a.rulePage
	value := strings.TrimSpace(st.entry)
	if value == "" {
		return
	}
	if key == "domain" {
		entries := ruleEntries(key, value)
		for i, entry := range entries {
			if !strings.HasPrefix(entry, "domain:") && !strings.HasPrefix(entry, "full:") && !strings.HasPrefix(entry, "geosite:") && !strings.HasPrefix(entry, "regexp:") && !strings.HasPrefix(entry, "keyword:") && !strings.HasPrefix(entry, "dotless:") && !strings.HasPrefix(entry, "ext:") {
				entries[i] = "domain:" + entry
			}
		}
		value = strings.Join(entries, "\n")
	}
	if err := validateRuleCondition(key, value); err != nil {
		st.err = err.Error()
		return
	}
	entries := ruleEntries(key, st.values[key]+"\n"+value)
	st.values[key] = strings.Join(entries, "\n")
	st.entry, st.err = "", ""
}

func (a *app) ruleConditionEntries(c *ui.Context, p palette, d ruleCondition) {
	st := &a.rulePage
	entries := ruleEntries(d.key, st.values[d.key])
	if d.key == "domain" {
		entries = slices.DeleteFunc(entries, func(v string) bool { return strings.HasPrefix(v, "geosite:") })
	}
	if len(entries) == 0 {
		return
	}
	st.entryList.Key = func(i int) any { return entries[i] }
	containListScroll(ui.List(c, &st.entryList, len(entries), func(i int) {
		value := entries[i]
		ui.Row(c.Key(value)).Padding(5, 6).Gap(5).AlignItems(ui.Center).Children(func() {
			label(c, value, sizeCaption, 16, 400, p.text).Grow(1).Basis(0).MinWidth(0).SingleLine()
			if ui.Button(c.Key("remove-entry"), tr("Remove")).Label(tr("Remove ") + value).Clicked() {
				var next []string
				for _, v := range ruleEntries(d.key, st.values[d.key]) {
					if v != value {
						next = append(next, v)
					}
				}
				st.values[d.key] = strings.Join(next, "\n")
			}
		})
	})).Label(tr(d.title) + tr(" entries")).Height(min(136, float32(len(entries))*40)).Shrink(0).Radius(6).Background(p.subdued)
}

// Use the same translucent card, recessed content and control surfaces as
// the main panel and subscription details, including over native material.
func ruleFormTheme(c *ui.Context, p palette, panel bool) ui.Theme {
	t := *c.Theme()
	// Tooltips fall back to Text/Background. Resolve those colors before
	// making the form background transparent, or their text disappears.
	if t.Inverse == ui.Transparent {
		t.Inverse = t.Text
	}
	if t.InverseText == ui.Transparent {
		t.InverseText = t.Background
	}
	t.FontSize = sizeSubhead
	t.Spacing = 3
	t.Radius = 6
	t.Text, t.TextMuted = p.text, p.muted
	t.Border = p.borderDefault
	t.Accent, t.AccentHover, t.AccentText = p.accent, p.accentHover, ui.Hex("#ffffff")
	t.InactiveAccent = p.accent
	t.Focus = p.borderFocus
	t.Background = ui.Transparent
	if panel {
		t.Background = p.subdued
	}
	t.Surface = p.cardHover
	t.SurfaceHover = p.cardActive
	t.SurfacePressed = p.cardActive

	return t
}

func ruleFieldColor(p palette) ui.Color { return p.subdued }

// Use MyGo's radio-group behavior, with equal-width segment triggers.
func ruleSegments(c *ui.Context, key string, selected *int, p palette, options ...string) bool {
	empty := *selected < 0
	parts := ui.SegmentedBase(c.Key(key), selected, len(options))
	if empty {
		*selected = -1
	}
	t := c.Theme()
	groupName := key
	switch key {
	case "app-scope":
		groupName = tr("Application source")
	case "ip-mode":
		groupName = tr("IP address source")
	case "domain-mode":
		groupName = tr("Domain matching mode")
	case "geo-scope":
		groupName = tr("Website list source")
	case "network":
		groupName = tr("Network type")
	case "destination-kind":
		groupName = tr("Destination")
	}
	parts.Track.Label(groupName).Padding(t.Space(0.5)).Gap(t.Space(0.5)).Radius(t.Radius).Background(ruleFieldColor(p)).Border(1, t.Border).Shrink(0).Children(func() {
		for i, title := range options {
			segment := ruleSegmentStyle(parts.Segment(i).Label(title), i == *selected, t)
			segment.Children(func() { ui.Text(c, title).SingleLine().TextAlign(ui.Center) })
		}
	})
	return parts.Track.Changed()
}

// Both native radio segments and multi-select toggles share the same surfaces.
func ruleSegmentStyle(segment ui.Element, selected bool, t *ui.Theme) ui.Element {
	segment = segment.Grow(1).Basis(0).MinWidth(0).Padding(t.Space(1), 0).Radius(max(t.Radius-t.Space(0.5), 0)).Cursor(ui.CursorPointer)
	if selected {
		segment.Background(t.SurfacePressed).Shadow(0, 1, 2, 0, ui.RGBA(0, 0, 0, 0.12))
	} else if segment.Hovered() {
		segment.Background(t.SurfaceHover)
	}
	return segment
}
