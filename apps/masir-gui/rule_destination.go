package main

import (
	"strings"

	"github.com/egoist/mygo/ui"
)

func (a *app) ruleDestinationAccordion(c *ui.Context, p palette) {
	st := &a.rulePage
	open := st.expanded == "destination"
	acc := ui.AccordionBase(c)
	acc.Root.Label(tr("Rule destination")).Radius(cardRadius).Background(p.card).Border(1, p.borderDefault).Clip().Shrink(0).Children(func() {
		item := acc.Item(&open)
		row := item.Trigger.Label(tr("Choose destination")).Disabled(st.busy).Height(40).Padding(0, 10).Gap(8).AlignItems(ui.Center).Cursor(ui.CursorPointer)
		if row.Hovered() {
			row.Background(p.cardHover)
		}
		row.Children(func() {
			label(c, tr("Target"), sizeSubhead, 16, 500, p.text).Grow(1).Basis(0).MinWidth(0)
			a.destinationBadge(c, p, st.target)
			ui.Icon(c, iconChevronRight).FontSize(12).TextColor(p.tertiary).Rotate(90 * item.Progress())
		})
		if row.Changed() && !st.busy {
			a.toggleRuleAccordion("destination")
		}
		item.Panel(func() {
			panelTheme := ruleFormTheme(c, p, true)
			body := ui.Themed(c, &panelTheme).Label(tr("Destination options")).Shrink(0)
			body.Children(func() {
				ui.Column(c).Padding(8).Gap(6).Children(func() {
					index := -1
					for i, target := range []string{"proxy", "direct", "block"} {
						if target == st.target {
							index = i
						}
					}
					if ruleSegments(c, "destination-kind", &index, p, tr("Proxy"), tr("Direct"), tr("Block")) {
						st.target = []string{"proxy", "direct", "block"}[index]
					}
					hint := tr("Proxy follows your active server.")
					switch st.target {
					case "direct":
						hint = tr("Connect without the proxy.")
					case "block":
						hint = tr("Reject matching connections.")
					default:
						if strings.HasPrefix(st.target, "config:") {
							hint = tr("Pinned to the selected server.")
						}
					}
					label(c, hint, sizeMicro, 15, 400, p.muted)
				})
				if len(a.subscriptions.servers) == 0 {
					label(c, tr("Add a subscription to pin a specific server."), sizeCaption, 16, 400, p.muted).Padding(8)
					return
				}
				panelBar(c, p, func() { searchInput(c, "destination-search", &a.ruleDestinationQuery, tr("Search servers…"), p) }, func() {})
				a.ruleDestinationServers(c, p)
			})
			// The parent scroll follows the native expansion immediately and stops
			// following once the accordion reaches rest.
			if st.revealTarget && open {
				st.targetRevealSettled = item.Progress() >= 1
			}

		})
	})
}

func (a *app) ruleDestinationServers(c *ui.Context, p palette) {
	query := strings.ToLower(strings.TrimSpace(a.ruleDestinationQuery))
	folds := &a.ruleServerFolds
	folds.update(query)
	groups := orderedGroups(a.serversMatching(query))
	var items []serverListItem
	height := float32(8)
	for i := range groups {
		g := &groups[i]
		items = append(items, serverListItem{group: g})
		height += 30
		if folds.collapsed(a, g.id) {
			continue
		}
		for _, s := range g.servers {
			items = append(items, serverListItem{srv: s})
			height += 38
		}
	}
	if len(items) == 0 {
		label(c, tr("No matching servers."), sizeCaption, 16, 400, p.muted).Padding(12, 8)
		return
	}
	height += float32(max(0, len(items)-1)) * 3
	a.ruleDestinations.Key = func(i int) any { return serverItemKey(items[i]) }
	a.ruleDestinations.Header = func(i int) bool { return items[i].group != nil }
	a.ruleDestinations.Label = func(i int) string {
		if items[i].group != nil {
			return items[i].group.name
		}
		return items[i].srv.cfg.Name
	}
	containListScroll(ui.List(c, &a.ruleDestinations, len(items), func(i int) {
		it := items[i]
		if it.group != nil {
			g := it.group
			a.groupHeader(c, p, g.id, g.name, len(g.servers), folds.collapsed(a, g.id), tr("Target server group ")+g.name, func() { folds.toggle(a, g.id) }, nil)
			return
		}
		a.ruleDestinationServer(c, p, it.srv)
	})).Label(tr("Target servers")).Height(min(180, height)).Shrink(0).Padding(4, 6).Gap(3).ScrollbarInsets(0, 1, 0)
}

func (a *app) ruleDestinationServer(c *ui.Context, p palette, s server) {
	target := "config:" + s.key.sub + "/" + s.key.cfg
	if a.serverListRow(c, p, s, tr("Pin ")+s.subName+" "+s.cfg.Name, a.rulePage.target == target, true, a.rulePage.busy) {
		a.rulePage.target = target
	}
}
