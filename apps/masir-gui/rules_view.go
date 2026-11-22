package main

import (
	"fmt"
	"runtime"
	"strings"
	"time"

	pb "github.com/Gozargah/Masir/libs/masir-proto/gen/masir/daemon/v1"
	"github.com/egoist/mygo/ui"
)

// ---- rules ----
func (a *app) rulesHeader(c *ui.Context, p palette) {
	enabled := a.rulesOn()
	count := len(a.ruleList())
	row := ui.ButtonBase(c.Key("rules-row")).Padding(0, tw(3)).MinHeight(32).Cursor(ui.CursorPointer).
		AlignItems(ui.Center).Justify(ui.SpaceBetween).Shrink(0)
	bg := ui.Color{}
	if row.Hovered() {
		bg = p.cardHover
	}
	tint(row, bg)
	row.Children(func() {
		ui.Row(c).Gap(tw(1.5)).AlignItems(ui.Center).Children(func() {
			ui.Box(c).Size(tw(4), tw(4)).Center().Shrink(0).Children(func() {
				ui.Icon(c, iconChevronRight).FontSize(13).TextColor(p.muted).Rotate(90 * a.motion.rules.x)
			})
			label(c, tr("RULES"), sizeMicro, 14, 500, p.muted).LetterSpacing(0.5)
		})
		ui.Row(c).Gap(tw(2)).AlignItems(ui.Center).Children(func() {
			noun := "rules"
			if count == 1 {
				noun = "rule"
			}
			ui.Row(c).Gap(tw(1)).AlignItems(ui.Center).Children(func() {
				rollingNumber(c, "rules-count", float64(count), fmt.Sprint(count), sizeCaption, 15, 400, p.muted, false)
				label(c, noun, sizeCaption, 15, 400, p.muted)
			})
			ui.Row(c).Margin(0, 0, 0, tw(1.5)).Children(func() {
				if next, changed := nativeSwitch(c, "rules-switch", tr("Use rules"), enabled, switchSubdued, false, false, p); changed {
					a.setRulesEnabled(next)
				}
			})
		})
	})
	if row.Clicked() {
		a.toggle(sectionRules)
	}
}

func (a *app) rulesPanel(c *ui.Context, p palette) {
	enabled := a.rulesOn()
	count := len(a.ruleList())
	panelBar(c, p, func() { searchInput(c, "rule-search", &a.ruleQuery, tr("Search rules..."), p) }, func() {
		ui.Row(c).Gap(tw(1)).AlignItems(ui.Center).Children(func() {
			if iconButton(c, "route-check", iconGlobe, tr("Check domain route"), p, iconMD, variantSubtle, 12) {
				a.openPage(routeCheckPath)
			}
			if iconButton(c, "rule-add", iconPlus, tr("Add rule"), p, iconMD, variantSubtle, 12) {
				a.newRule()
			}
		})
	})
	// The list builds only the rows it shows. Scrolling it closes an open row and menu.
	first, last := a.rulesList.Visible()
	if a.lastRuleTop >= 0 && (first != a.lastRuleTop || last != a.lastRuleEnd) {
		a.menu.open = false
		if sw := a.swipes[a.openSwipe]; sw != nil {
			sw.close(c.Now())
		}
		if first >= 0 && last >= first && a.ruleScrollRestored && a.ruleQuery == "" {
			a.ruleScrollRow = first
			if first < len(a.ruleItems) {
				a.ruleScrollKey = ruleItemKey(a.ruleItems[first])
			}
			a.saveGUISettings()
		}
	}
	a.lastRuleTop, a.lastRuleEnd = first, last

	a.ruleItems = a.ruleListItems(a.filteredRules())
	maxHeight := 212 - (2*tw(2) + tw(7) + 1)
	height := a.ruleListH
	if height <= 0 {
		height = maxHeight
	}
	if len(a.ruleItems) == 0 {
		msg := tr("No rules configured.")
		if count > 0 {
			msg = tr("No matching rules found.")
		}
		empty := ui.Column(c.Key("rule-empty")).AlignItems(ui.Center).Justify(ui.Center).Children(func() {
			label(c, msg, sizeSubhead, 18, 400, p.muted).TextAlign(ui.Center)
		})
		if count > 0 {
			empty.Height(height) // keep the open list's height while a search narrows it
		} else {
			empty.Padding(10, 0)
		}
		return
	}
	ruleQueryCleared := a.ruleQuery == "" && a.lastRuleQuery != ""
	a.lastRuleQuery = a.ruleQuery
	if (!a.ruleScrollRestored || ruleQueryCleared) && len(a.ruleItems) > 0 {
		target := a.findRuleScrollRow(a.ruleItems, a.ruleScrollKey, a.ruleScrollRow)
		if target > 0 {
			a.rulesList.ScrollTo(target, ui.Start)
		}
		a.ruleScrollRestored = true
		a.lastRuleTop = target
		a.lastRuleEnd = target
	}
	list := containListScroll(ui.List(c, &a.rulesList, len(a.ruleItems), func(i int) {
		it := a.ruleItems[i]
		if it.rule == nil {
			a.ruleSectionRow(c, p, it.title, it.count)
			return
		}
		a.ruleRow(c, p, it.rule)
	})).Padding(0, tw(1.5), tw(1), tw(1)).Gap(tw(1.5)).Shrink(0)
	if a.ruleQuery == "" {
		list.MaxHeight(maxHeight)
		if h := list.Bounds().H; h > 0 {
			a.ruleListH = h
		}
	} else {
		list.Height(height)
	}
	if !enabled {
		list.Opacity(0.5)
	}
}

// ruleListItems turns the grouped rules into the flat rows the list builds: a header for
// each section, then its rules.
func (a *app) ruleListItems(rules []*pb.RoutingRule) []ruleListItem {
	sections := a.groupRules(rules)
	items := make([]ruleListItem, 0, len(rules)+len(sections))
	for _, s := range sections {
		items = append(items, ruleListItem{title: s.title, count: len(s.rules)})
		collapsed := a.isRuleSectionCollapsed(s.title) && a.ruleQuery == ""
		if !collapsed {
			for _, r := range s.rules {
				items = append(items, ruleListItem{rule: r})
			}
		}
	}
	return items
}

// ruleSectionRow is a section's title in the rules list; the list keeps it at the top
// while its rows scroll under it. Its background is opaque, unlike the list's tint: rows
// passing under a translucent header would show through it.
func (a *app) ruleSectionRow(c *ui.Context, p palette, title string, count int) {
	collapsed := a.isRuleSectionCollapsed(title) && a.ruleQuery == ""
	ui.Column(c).Background(p.listHeader).Children(func() {
		ui.Row(c).Padding(tw(1), tw(2), tw(0.5)).Radius(tw(1)).AlignItems(ui.Center).Shrink(0).Children(func() {
			chevron := iconChevronDown
			collapseTitle := tr("Collapse ") + tr(title)
			if collapsed {
				chevron = iconChevronRight
				collapseTitle = tr("Expand ") + tr(title)
			}
			if iconButton(c, "rule-section-collapse-"+title, chevron, collapseTitle, p, iconXS, variantGhost, 11) {
				a.toggleRuleSectionCollapse(title)
			}
			btn := ui.ButtonBase(c.Key("rule-section-" + title)).Grow(1).Basis(0).MinWidth(0).Clip().Cursor(ui.CursorPointer)
			btn.Children(func() {
				label(c, fmt.Sprintf("%s (%d)", strings.ToUpper(ltr(tr(title))), count), sizeMicro, 18, 600, p.muted).LetterSpacing(0.5).SingleLine().Grow(1).MinWidth(0)
			})
			if btn.Clicked() {
				a.toggleRuleSectionCollapse(title)
			}
		})
	})
}

func (a *app) swipeFor(tag string) *swipe {
	s := a.swipes[tag]
	if s == nil {
		s = &swipe{}
		a.swipes[tag] = s
	}
	return s
}

// ruleRow is a rule of the list: the row itself, which a two-finger swipe left slides over
// the buttons behind it, and a right-click menu.
func (a *app) ruleRow(c *ui.Context, p palette, r *pb.RoutingRule) {
	now := c.Now()
	sw := a.swipeFor(r.RuleTag)
	switch {
	case sw.open && a.openSwipe != r.RuleTag && sw.mode != swipeTracking && a.openSwipe != "":
		sw.close(now) // only one row is open
	case sw.open:
		a.openSwipe = r.RuleTag
	case a.openSwipe == r.RuleTag && sw.mode == swipeIdle:
		a.openSwipe = ""
	}
	if sw.advance(now) {
		c.Invalidate()
	}

	box := ui.Box(c.Key("rule-" + r.RuleTag)).MinHeight(tw(10)).Radius(tw(2)).Clip().Shrink(0)
	box.Children(func() {
		if sw.x != 0 {
			a.swipeButtons(c, p, r, sw, now)
		}
		row := ui.Row(c.Key("rule-row")).Padding(tw(1.5), tw(1), tw(1.5), tw(1.5)).
			AlignItems(ui.Center).Justify(ui.SpaceBetween).Shrink(0).Translate(sw.x, 0)
		bg := ui.Color{}
		if row.Hovered() || (a.menu.open && a.menu.tag == r.RuleTag) {
			bg = p.cardHover
			if a.menu.open && a.menu.tag == r.RuleTag {
				bg = p.cardActive
			}
		}
		tint(row, bg)
		if r.Disabled {
			row.Opacity(0.5)
		}
		// Two fingers on the touchpad swipe the row; a mouse wheel and the vertical scrolling
		// of the list are left alone.
		row.HandleInput(func(ev ui.InputEvent) bool {
			if ev.Kind != ui.InputScroll || !ev.Precise {
				return false
			}
			return sw.scroll(ev.DX, ev.DY, ev.Time)
		})
		handled := false
		row.Children(func() {
			a.ruleContent(c, p, r)
			// A touchpad swipe is macOS's gesture; elsewhere the row's actions have a
			// button of their own.
			if runtime.GOOS != "darwin" && iconButton(c, "more-"+r.RuleTag, iconMore, tr("Rule actions"), p, iconXS, variantGhost, 12) {
				handled = true
				b := row.Bounds()
				a.menu = ruleMenu{open: true, tag: r.RuleTag, x: b.X + b.W - menuWidth - tw(2), y: b.Y + b.H}
				sw.close(now)
			}
		})
		if row.Clicked() && !handled {
			if sw.open {
				sw.close(now)
			} else {
				a.openRule(r)
			}
		}
		if row.RightClicked() {
			px, py, _ := row.PointerPosition()
			b := row.Bounds()
			a.menu = ruleMenu{open: true, tag: r.RuleTag, x: b.X + px, y: b.Y + py}
			sw.close(now)
		}
	})
}

// ruleContent is what a rule's row shows: the tile, the title, and where it goes.
func (a *app) ruleContent(c *ui.Context, p palette, r *pb.RoutingRule) {
	info := a.metaFor(r).info
	var icon *ui.Bitmap
	if info.appPath != "" {
		meta := a.apps.lookup(info.appPath, a.async, func() { a.post(func() {}) })
		info.title, icon = a.apps.read(meta)
	} else if info.flag == "" && info.domain != "" {
		icon = a.favicons.lookup(info.domain, a.async, func() { a.post(func() {}) })
	}
	ui.Row(c).Gap(tw(2.5)).AlignItems(ui.Center).Grow(1).Basis(0).MinWidth(0).MarginEnd(tw(1.5)).Children(func() {
		iconTile(c, p, func() {
			switch {
			case icon != nil && info.appPath != "":
				ui.Image(c, icon).Size(tw(7), tw(7)).Radius(4).MarginY(-4).Label(tr("Rule application icon"))
			case icon != nil:
				ui.Image(c, icon).Size(18, 18).Radius(4)
			case info.flag != "":
				flagElement(c, info.flag, 18)
			default:
				ui.Icon(c, info.icon).FontSize(14).TextColor(p.muted)
			}
		})
		ui.Row(c).Gap(tw(1.5)).AlignItems(ui.Center).Grow(1).Basis(0).MinWidth(0).Children(func() {
			ltrLabel(c, info.title, sizeBody, 18, 500, p.text)
			if info.summary != "" {
				mono(c, "· "+info.summary, sizeCaption, 14, 400, p.muted).SingleLine().Shrink(0)
			}
		})
	})
	a.destinationBadge(c, p, r.Target)
}

// swipeButtons are the buttons behind a row: they slide in from the right as the row slides
// away, fading in over the first few units.
func (a *app) swipeButtons(c *ui.Context, p palette, r *pb.RoutingRule, sw *swipe, now time.Time) {
	bar := ui.Row(c.Key("swipe-buttons")).Absolute().Top(0).Bottom(0).Right(0).Padding(0, tw(1.5), 0, 0).
		Gap(tw(1.5)).AlignItems(ui.Center).Translate(sw.buttonsX(), 0).Opacity(sw.buttonsOpacity())
	bar.Children(func() {
		if sw.confirmRemove {
			if ruleRemoveButton(c, p, "confirm-swipe-remove", tw(7), false) {
				sw.close(now)
				a.removeRule(r.RuleTag)
			}
			return
		}
		title := tr("Disable Rule")
		if r.Disabled {
			title = tr("Enable Rule")
		}
		if a.swipeButton(c, p, "power", title, iconPower, false, !r.Disabled) {
			sw.close(now)
			a.setRuleEnabled(r.RuleTag, r.Disabled)
		}
		if a.swipeButton(c, p, "edit", tr("Edit Rule"), iconPencil, false, true) {
			sw.close(now)
			a.editRule(r)
		}
		if a.swipeButton(c, p, "delete", tr("Remove Rule"), iconTrash, true, true) {
			sw.confirmRemove = true
		}
	})
}

// swipeButton is one of the square buttons: quiet, and red under the pointer for removing.
func (a *app) swipeButton(c *ui.Context, p palette, key, title string, icon *ui.SVG, danger, active bool) bool {
	b := ui.ButtonBase(c.Key(key)).Label(title).Tooltip(title).Size(tw(7), tw(7)).Radius(6).Center().Shrink(0).
		Cursor(ui.CursorPointer)
	hover := b.Hovered()
	bg, border, fg := p.subdued, p.borderDefault, p.muted
	switch {
	case danger:
		bg, border = p.cardHover, p.borderSubtle
		if hover {
			bg, border, fg = p.errorSurface, p.errorBorder, p.errorC
		}
	case !active:
		bg, border, fg = p.card, p.borderSubtle, p.tertiary
	case hover:
		bg, fg = p.cardActive, p.text
	}
	b.Background(bg).Border(1, border)
	b.Children(func() { ui.Icon(c, icon).FontSize(13).TextColor(fg) })
	return b.Clicked()
}

// destinationBadge says where a rule's traffic goes.
func (a *app) destinationBadge(c *ui.Context, p palette, target string) {
	switch {
	case target == "block":
		badge(c, tr("Block"), iconBan, p.errorC, p.errorSurface, tw(7), 12)
	case target == "proxy":
		badge(c, tr("Proxy"), iconShuffle, p.healthy, p.healthySurface, tw(7), 12)
	case target == "direct":
		badge(c, tr("Direct"), iconDirect, p.direct, p.directSurface, tw(7), 12)
	default:
		flag, name := "🌐", strings.TrimPrefix(strings.TrimPrefix(target, "outbound:"), "config:")
		if strings.HasPrefix(target, "config:") {
			flag, name = a.pinnedServer(strings.TrimPrefix(target, "config:"))
		}
		ui.Row(c).Gap(tw(1)).Padding(tw(0.5), tw(1.5)).Radius(6).Background(p.pinnedSurface).Height(tw(7)).AlignItems(ui.Center).Shrink(0).Children(func() {
			flagElement(c, flag, 14)
			label(c, ltr(name), sizeSubhead, 14, 500, p.pinned).SingleLine().MaxWidth(120)
		})
	}
}

// pinnedServer is the flag and country of the server a "sub/cfg" target points at.
func (a *app) pinnedServer(ref string) (flag, name string) {
	sub, cfg, _ := strings.Cut(ref, "/")
	for _, s := range a.subscriptions.servers {
		if s.key.sub == sub && s.key.cfg == cfg {
			f, t, _ := serverTitle(s.cfg.Name)
			return f, t
		}
	}
	return "🌐", ref
}

func ruleItemKey(it ruleListItem) string {
	if it.rule == nil {
		return "h:" + it.title
	}
	return "r:" + it.rule.RuleTag
}

func (a *app) findRuleScrollRow(items []ruleListItem, key string, savedRow int) int {
	if key != "" {
		for i, it := range items {
			if ruleItemKey(it) == key {
				return i
			}
		}
		if strings.HasPrefix(key, "r:") {
			tag := key[2:]
			if r := a.ruleByTag(tag); r != nil {
				secKey := "h:" + a.metaFor(r).info.section
				for i, it := range items {
					if ruleItemKey(it) == secKey {
						return i
					}
				}
			}
		}
	}
	if savedRow > 0 {
		if savedRow >= len(items) {
			return len(items) - 1
		}
		return savedRow
	}
	return -1
}
