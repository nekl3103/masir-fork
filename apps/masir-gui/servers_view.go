package main

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"

	pb "github.com/Gozargah/Masir/libs/masir-proto/gen/masir/daemon/v1"
)

// maxServerListHeight caps the picker; below it, the list keeps the height it has with
// no search while a search narrows its rows.
const maxServerListHeight float32 = 300

func (a *app) serverPicker(c *ui.Context, p palette) {
	query := strings.ToLower(strings.TrimSpace(a.query))
	folds := &a.mainServerFolds
	folds.update(query)
	servers := a.serversMatching(query)
	groups := orderedGroups(servers)

	panelBar(c, p, func() { searchInput(c, "server-search", &a.query, tr("Search servers…"), p) }, func() {
		ui.Row(c).Gap(tw(1)).AlignItems(ui.Center).Children(func() {
			busy := len(a.testing) > 0
			if iconButton(c, "test-all", iconZap, map[bool]string{true: tr("Testing latency..."), false: tr("Test Latency (All nodes)")}[busy], p, iconMD, variantSubtle, 12) && !busy {
				a.testServers()
			}
		})
	})
	height := a.serverListH
	if height <= 0 {
		height = maxServerListHeight
	}
	if len(servers) == 0 {
		ui.Column(c.Key("server-empty")).Height(height).AlignItems(ui.Center).Justify(ui.Center).Gap(tw(1)).Children(func() {
			label(c, tr("No matching servers"), sizeSubhead, 18, 500, p.muted)
			label(c, tr("Try a different search term"), sizeMicro, 14, 400, p.tertiary)
		})
		return
	}
	active, _ := a.activeKey()
	items := make([]serverListItem, 0, len(servers)+len(groups))
	for i := range groups {
		g := &groups[i]
		items = append(items, serverListItem{group: g})
		collapsed := folds.collapsed(a, g.id)
		for _, s := range g.servers {
			if !collapsed || s.key == active {
				items = append(items, serverListItem{srv: s})
			}
		}
	}
	a.serverItems = items
	queryCleared := a.query == "" && a.lastServerQuery != ""
	a.lastServerQuery = a.query
	if a.scrollToSubID != "" && len(items) > 0 {
		target := -1
		for i, it := range items {
			if it.group != nil && it.group.id == a.scrollToSubID {
				target = i
				break
			}
		}
		if target >= 0 {
			a.serversList.ScrollTo(target, ui.Start)
			a.serverScrollRow = target
			a.serverScrollKey = serverItemKey(items[target])
			a.lastServerTop = target
			a.scrollToSubID = ""
			a.serverScrollRestored = true
		}
	} else if (!a.serverScrollRestored || queryCleared) && len(items) > 0 {
		target := findServerScrollRow(items, a.serverScrollKey, a.serverScrollRow)
		if target > 0 {
			a.serversList.ScrollTo(target, ui.Start)
		}
		a.serverScrollRestored = true
		a.lastServerTop = target
	}
	list := containListScroll(ui.List(c, &a.serversList, len(items), func(i int) {
		it := a.serverItems[i]
		if it.group != nil {
			g := it.group
			var openDetails func()
			if g.id != "custom" {
				openDetails = func() { a.openSubscription(g.id) }
			}
			ui.Column(c).Background(p.listHeader).Children(func() {
				a.groupHeader(c, p, g.id, g.name, len(g.servers), folds.collapsed(a, g.id), tr("Server group ")+g.name, func() { folds.toggle(a, g.id) }, openDetails)
				for _, sub := range a.subscriptions.subscriptions {
					if sub.Id == g.id && sub.LastError != "" {
						label(c, tr("Refresh failed: ")+sub.LastError, sizeCaption, 15, 400, p.errorC).Padding(0, tw(2), tw(1)).MaxLines(2)
					}
				}
			})
			return
		}
		a.serverItem(c, p, it.srv)
	})).Padding(0, tw(1.5), 0, tw(1)).Gap(2).Shrink(0)
	if a.query == "" {
		list.MaxHeight(maxServerListHeight)
		if h := list.Bounds().H; h > 0 {
			a.serverListH = h
		}
	} else {
		list.Height(height)
	}
	first, last := a.serversList.Visible()
	if first >= 0 && last >= first && a.serverScrollRestored && a.query == "" {
		if a.lastServerTop >= 0 && first != a.lastServerTop {
			a.lastServerTop = first
			a.serverScrollRow = first
			if first < len(a.serverItems) {
				a.serverScrollKey = serverItemKey(a.serverItems[first])
			}
			a.saveGUISettings()
		} else if a.lastServerTop < 0 {
			a.lastServerTop = first
		}
	}
}

func serverItemKey(it serverListItem) string {
	if it.group != nil {
		return "g:" + it.group.id
	}
	return "s:" + it.srv.key.sub + ":" + it.srv.key.cfg
}

func findServerScrollRow(items []serverListItem, key string, savedRow int) int {
	if key != "" {
		for i, it := range items {
			if serverItemKey(it) == key {
				return i
			}
		}
		if strings.HasPrefix(key, "s:") {
			parts := strings.SplitN(key[2:], ":", 2)
			if len(parts) > 0 {
				groupKey := "g:" + parts[0]
				for i, it := range items {
					if serverItemKey(it) == groupKey {
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

type serverGroup struct {
	id      string
	name    string
	servers []server
}

// serverListItem is one row of the server picker: a subscription's header, or a server.
type serverListItem struct {
	group *serverGroup
	srv   server
}

// orderedGroups gathers servers by subscription, in the order the subscriptions come.
func orderedGroups(servers []server) []serverGroup {
	var out []serverGroup
	idx := map[string]int{}
	for _, s := range servers {
		name := s.subName
		if name == "" {
			name = tr("Custom Outbounds")
		}
		subID := s.key.sub
		if subID == "" {
			subID = "custom"
		}
		i, ok := idx[subID]
		if !ok {
			i = len(out)
			idx[subID] = i
			out = append(out, serverGroup{id: subID, name: name})
		}
		out[i].servers = append(out[i].servers, s)
	}
	return out
}

// Search initially reveals matching servers. Folds made while searching
// apply to that search and leave the saved subscription folds intact.
type serverSearchFolds struct {
	query  string
	groups map[string]bool
}

func (f *serverSearchFolds) update(query string) {
	if f.query != query {
		f.query = query
		f.groups = nil
	}
}

func (f *serverSearchFolds) collapsed(a *app, id string) bool {
	if f.query == "" {
		return a.isGroupCollapsed(id)
	}
	return f.groups[id]
}

func (f *serverSearchFolds) toggle(a *app, id string) {
	if f.query == "" {
		a.toggleGroupCollapse(id)
		return
	}
	if f.groups == nil {
		f.groups = make(map[string]bool)
	}
	f.groups[id] = !f.groups[id]
}

func (a *app) groupHeader(c *ui.Context, p palette, id, title string, countServers int, collapsed bool, headerLabel string, toggle, openDetails func()) {
	ui.Column(c).Label(headerLabel).Background(p.listHeader).Shrink(0).Children(func() {
		ui.Row(c).Padding(tw(1), tw(2), tw(1), tw(1)).Gap(tw(1.5)).AlignItems(ui.Center).Shrink(0).Children(func() {
			drawTitle := func() {
				label(c, strings.ToUpper(ltr(title)), sizeMicro, 18, 600, p.muted).LetterSpacing(0.5).SingleLine().Grow(1).MinWidth(0)
			}
			n := fmt.Sprintf(tr("%d nodes"), countServers)
			if countServers == 1 {
				n = tr("1 node")
			}
			drawCount := func() { label(c, n, sizeCaption, 18, 400, p.muted) }
			if toggle == nil {
				ui.Row(c).Height(22).PaddingStart(tw(1)).AlignItems(ui.Center).Grow(1).Basis(0).MinWidth(0).Clip().Children(drawTitle)
				drawCount()
			} else {
				chevron := iconChevronDown
				collapseTitle := tr("Collapse ") + title
				if collapsed {
					chevron = iconChevronRight
					collapseTitle = tr("Expand ") + title
				}
				name := ui.ButtonBase(c.Key("group-collapse-" + id)).Label(collapseTitle).Height(22).
					Gap(tw(1)).AlignItems(ui.Center).Grow(1).Basis(0).MinWidth(0).Clip().Cursor(ui.CursorPointer)
				name.Children(func() {
					ui.Icon(c, chevron).Size(12, 12).TextColor(p.muted).Shrink(0)
					drawTitle()
				})
				count := ui.ButtonBase(c.Key("group-count-" + id)).Cursor(ui.CursorPointer).Shrink(0)
				count.Children(drawCount)
				if name.Clicked() || count.Clicked() {
					toggle()
				}
			}
			if iconButton(c, "group-test-"+id, iconZap, tr("Test all ")+title+tr(" configs"), p, iconXS, variantGhost, 10) {
				var targets []server
				for _, s := range a.subscriptions.servers {
					if s.key.sub == id || (id == "custom" && s.key.sub == "") {
						targets = append(targets, s)
					}
				}
				if len(targets) > 0 {
					a.testServers(targets...)
				}
			}
			if openDetails != nil && iconButton(c, "group-details-"+id, iconChevronRight, tr("Open ")+title+tr(" subscription"), p, iconXS, variantGhost, 11) {
				openDetails()
			}
		})
	})
}

func (a *app) serverItem(c *ui.Context, p palette, s server) {
	active, ok := a.activeKey()
	if a.serverListRow(c, p, s, tr("Use ")+s.cfg.GetName(), ok && active == s.key, false, false) {
		a.selectServer(s)
	}
}

func (a *app) serverListRow(c *ui.Context, p palette, s server, action string, selected, pinned, disabled bool) bool {
	row := ui.ButtonBase(c.Key(s.key)).Label(action).Disabled(disabled).
		Padding(tw(1.5), tw(1), tw(1.5), tw(2)).Radius(tw(2)).MinHeight(38).Cursor(ui.CursorPointer).
		AlignItems(ui.Center).Justify(ui.SpaceBetween).Shrink(0)
	bg := ui.Color{}
	switch {
	case a.menu.open && a.menu.config != nil && a.menu.configKey == s.key:
		bg = p.cardHover
	case selected && pinned:
		bg = p.pinnedSurface
	case selected:
		bg = p.healthySurface
	case row.Hovered():
		bg = p.cardHover
	}
	tint(row, bg)
	probe := false
	row.Children(func() {
		serverIdentity(c, p, s)
		ui.Row(c).Gap(tw(1.5)).AlignItems(ui.Center).Shrink(0).Children(func() {
			if selected && pinned {
				ui.Icon(c, iconCheck).FontSize(12).TextColor(p.pinned).Shrink(0)
			}
			probe = latencyBadge(c, "lat", a.latency[s.key], a.testing[s.key], p)
		})
	})
	if probe {
		a.testServers(s)
	}
	if row.RightClicked() {
		px, py, _ := row.PointerPosition()
		b := row.Bounds()
		a.menu = ruleMenu{open: true, configKey: s.key, config: s.cfg, x: b.X + px, y: b.Y + py}
	}
	return row.Clicked() && !probe
}

// serverIdentity gives server rows a common flag and title layout.
func serverIdentity(c *ui.Context, p palette, s server) {
	flag, title, detail := serverTitle(s.cfg.Name)
	ui.Row(c).Gap(tw(2.5)).AlignItems(ui.Center).Grow(1).Basis(0).MinWidth(0).MarginEnd(tw(2)).Children(func() {
		iconTile(c, p, func() { flagElement(c, flag, 18) })
		ui.Column(c).Grow(1).Basis(0).MinWidth(0).Children(func() {
			ltrLabel(c, title, sizeBody, 16, 600, p.text)
			if detail != "" {
				ltrLabel(c, detail, sizeCaption, 14, 400, p.muted)
			}
		})
	})
}

// serverRow shows the selected server, or guidance when none is selected.
func (a *app) serverRow(c *ui.Context, p palette) {
	s, ok := a.active()
	row := ui.ButtonBase(c.Key("server-row")).Padding(tw(2.5), tw(3)).Gap(tw(2)).Cursor(ui.CursorPointer).
		AlignItems(ui.Center).Justify(ui.SpaceBetween).Shrink(0)
	bg := ui.Color{}
	if row.Hovered() {
		bg = p.cardHover
	}
	tint(row, bg)
	row.Children(func() {
		flag, title, detail := serverTitle(s.cfg.GetName())
		if !ok {
			flag, title, detail = "🌐", tr("Select a server"), ""
		}
		ui.Column(c).Grow(1).Basis(0).MinWidth(0).Children(func() {
			ui.Row(c).AlignItems(ui.Center).Gap(tw(1)).Children(func() {
				flagElementSize(c, flag, 13*4.0/3, 13)
				ltrLabel(c, title, sizeTitle, 17, 600, p.text)
			})
			subtitle := tr("Choose a server to connect")
			if !ok && len(a.subscriptions.servers) == 0 {
				subtitle = tr("Add a subscription to get started")
				if !a.reachable {
					subtitle = tr("Waiting for available servers")
				}
			}
			if ok {
				sub := s.subName
				extra := detail
				if extra == "" {
					extra = tr("Default server")
					if a.fallback() == pb.Fallback_FALLBACK_DIRECT {
						extra = tr("Proxy server")
					}
				}
				subtitle = sub + " • " + extra
			}
			ltrLabel(c, subtitle, sizeCaption, 14, 400, p.muted).MaxWidth(180)
		})
		ui.Row(c).Gap(tw(1)).AlignItems(ui.Center).Shrink(0).Children(func() {
			if ok {
				if latencyBadge(c, "active-latency", a.latency[s.key], a.testing[s.key], p) {
					a.testServers(s)
				}
			}
			ui.Box(c).Size(tw(4), tw(4)).Center().Shrink(0).Children(func() {
				ui.Icon(c, iconChevronDown).FontSize(15).TextColor(p.tertiary).Rotate(180 * a.motion.servers.x)
			})
		})
	})
	if row.Clicked() {
		a.toggle(sectionServers)
	}
}
