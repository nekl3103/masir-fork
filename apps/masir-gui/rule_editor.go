package main

import (
	"encoding/json"
	"fmt"
	"math"
	"net/netip"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	pb "github.com/Gozargah/Masir/libs/masir-proto/gen/masir/daemon/v1"
	"github.com/egoist/mygo/ui"
)

type ruleCondition struct {
	key, title, hint string
	icon             *ui.SVG
}

var ruleConditions = []ruleCondition{
	{"process", "Applications", "One executable name or full path per line. A trailing / includes everything inside a directory.", iconApp},
	{"domain", "Websites and domains", "One entry per line: domain:example.com (with subdomains), full:example.com, regexp:… or geosite:category.", iconGlobe},
	{"ip", "IP addresses and ranges", "IP addresses, CIDR ranges or geoip:category separated by commas.", iconLayers},
	{"port", "Target ports", "Ports or ranges separated by commas, such as 80,443,1000-2000.", iconHash},
	{"network", "Network type", "Restrict the rule to TCP or UDP traffic.", iconShuffle},
	{"protocol", "Application protocols", "One protocol per line: http, tls, quic or bittorrent. Requires protocol sniffing.", iconLayers},
	{"source", "Source addresses", "Source IP addresses or CIDR ranges separated by commas.", iconLayers},
	{"sourcePort", "Source ports", "Ports or ranges separated by commas.", iconHash},
}

type rulePageState struct {
	formScroll                          ui.ScrollState
	targetRevealSettled                 bool
	invalid                             bool
	loaded                              map[string]string
	raw                                 map[string]json.RawMessage
	original, name, target              string
	values                              map[string]string
	extra                               map[string]json.RawMessage
	expanded, entry, err                string
	domainMode, ipMode                  int
	revealTarget                        bool
	entryList                           ui.ListState
	editHeight                          float32
	editHeader, editContent, editFooter ui.Handle
	busy, more, removing                bool
}

func decodeRule(r *pb.RoutingRule) rulePageState {
	st := rulePageState{target: "proxy", values: map[string]string{}, extra: map[string]json.RawMessage{}}
	if r == nil {
		return st
	}
	st.original, st.name, st.target = r.RuleTag, r.RuleTag, r.Target
	if err := json.Unmarshal([]byte(r.RuleJson), &st.extra); err != nil || st.extra == nil {
		st.invalid = true
		st.err = tr("This rule cannot be edited because its match data is invalid.")
		return st
	}
	for _, d := range ruleConditions {
		raw, ok := st.extra[d.key]
		if !ok {
			continue
		}
		var list []string
		var one string
		if json.Unmarshal(raw, &list) == nil {
			st.values[d.key] = strings.Join(list, "\n")
		} else if json.Unmarshal(raw, &one) == nil {
			st.values[d.key] = one
		} else {
			continue
		}
		delete(st.extra, d.key)
	}
	st.loaded = map[string]string{}
	st.raw = map[string]json.RawMessage{}
	for k, v := range st.values {
		st.loaded[k] = v
	}
	_ = json.Unmarshal([]byte(r.RuleJson), &st.raw)
	return st
}
func (a *app) newRule() {
	a.rulePage = decodeRule(nil)
	a.loadRuleApps()
	a.prepareRuleGeo()
	a.openPage(ruleEditPath)
}
func (a *app) editRule(r *pb.RoutingRule) {
	a.rulePage = decodeRule(r)
	a.loadRuleApps()
	a.prepareRuleGeo()
	a.openPage(ruleEditPath)
}
func (a *app) openRule(r *pb.RoutingRule) {
	a.ruleReview = decodeRule(r)
	a.ruleReview.more = true
	a.rulePage = decodeRule(r)
	a.openPage(ruleDetailsPath)
}

func ruleEntries(key, value string) []string {
	split := func(r rune) bool { return r == '\n' || r == '\r' || (key != "process" && r == ',') }
	var out []string
	seen := map[string]bool{}
	var parts []string
	if key == "domain" {
		// A regex may contain commas, including repetition counts such as {1,3}.
		// Keep a regexp: matcher intact; batches of hostnames can still use commas.
		for _, line := range strings.Split(value, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "regexp:") {
				parts = append(parts, line)
			} else {
				parts = append(parts, strings.FieldsFunc(line, split)...)
			}
		}
	} else {
		parts = strings.FieldsFunc(value, split)
	}
	for _, v := range parts {
		v = strings.TrimSpace(v)
		if v != "" && !seen[v] {
			out = append(out, v)
			seen[v] = true
		}
	}
	return out
}
func validateRuleCondition(key, value string) error {
	entries := ruleEntries(key, value)
	for _, v := range entries {
		switch key {
		case "port", "sourcePort":
			parts := strings.Split(v, "-")
			if len(parts) > 2 {
				return fmt.Errorf("%s", tr("Use a port or range, such as 443 or 1000-2000."))
			}
			prev := 0
			for _, s := range parts {
				n, err := strconv.Atoi(s)
				if err != nil || n < 1 || n > 65535 || n < prev {
					return fmt.Errorf("%s", tr("Ports must be between 1 and 65535, with ranges in ascending order."))
				}
				prev = n
			}
		case "ip", "source":
			if key == "ip" && strings.HasPrefix(v, "geoip:") && len(v) > 6 {
				continue
			}
			if _, err := netip.ParseAddr(v); err != nil {
				if _, err := netip.ParsePrefix(v); err != nil {
					return fmt.Errorf(tr("%q is not an IP address or CIDR range."), v)
				}
			}
		case "network":
			if v != "tcp" && v != "udp" {
				return fmt.Errorf("%s", tr("Choose TCP, UDP or Any."))
			}
		case "protocol":
			if v != "http" && v != "tls" && v != "quic" && v != "bittorrent" {
				return fmt.Errorf("%s", tr("Choose http, tls, quic or bittorrent."))
			}
		case "domain":
			if strings.HasPrefix(v, "regexp:") {
				if _, err := regexp.Compile(strings.TrimPrefix(v, "regexp:")); err != nil {
					return fmt.Errorf(tr("Invalid domain regular expression: %v"), err)
				}
				continue
			}
			if strings.ContainsAny(v, " \t/") || strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(v, "domain:"), "full:"), "geosite:")) == "" || strings.Contains(v, "://") {
				return fmt.Errorf("%s", tr("Enter a domain or category, such as domain:example.com or geosite:cn."))
			}
		}
	}
	return nil
}
func (st *rulePageState) matchJSON() (string, error) {
	if st.invalid {
		return "", fmt.Errorf("%s", tr("This rule has invalid match data and cannot be saved."))
	}
	m := map[string]json.RawMessage{}
	for k, v := range st.extra {
		m[k] = v
	}
	for _, d := range ruleConditions {
		value := st.values[d.key]
		if original, ok := st.loaded[d.key]; ok && original == value {
			m[d.key] = st.raw[d.key]
			continue
		}
		if err := validateRuleCondition(d.key, value); err != nil {
			return "", fmt.Errorf("%s: %w", tr(d.title), err)
		}
		entries := ruleEntries(d.key, value)
		if len(entries) == 0 {
			continue
		}
		var v any = entries
		if d.key == "port" || d.key == "sourcePort" || d.key == "network" {
			v = strings.Join(entries, ",")
		}
		m[d.key], _ = json.Marshal(v)
	}
	if len(m) == 0 {
		return "", fmt.Errorf("%s", tr("Add at least one matching condition."))
	}
	b, err := json.Marshal(m)
	return string(b), err
}
func (a *app) saveRule() {
	st := &a.rulePage
	if st.busy {
		return
	}
	if strings.TrimSpace(st.entry) != "" {
		a.addRuleConditionEntry(st.expanded)
		if st.entry != "" {
			return
		}
	}
	name := strings.TrimSpace(st.name)
	if name == "" {
		st.err = tr("Give this rule a name.")
		return
	}
	if r := a.ruleByTag(name); r != nil && name != st.original {
		st.err = tr("A rule with this name already exists.")
		return
	}
	for _, d := range ruleConditions {
		if original, ok := st.loaded[d.key]; ok && original == st.values[d.key] {
			continue
		}
		if err := validateRuleCondition(d.key, st.values[d.key]); err != nil {
			st.expanded = d.key
			st.more = st.more || d.key == "source" || d.key == "sourcePort" || d.key == "protocol"
			st.err = err.Error()
			return
		}
	}
	match, err := st.matchJSON()
	if err != nil {
		st.err = err.Error()
		return
	}
	original, target := st.original, st.target
	st.busy, st.err = true, ""
	a.async(func() {
		ctx, cancel := rpc()
		defer cancel()
		var err error
		if original == "" {
			_, err = a.d.AddRouting(ctx, &pb.AddRoutingRequest{RuleTag: name, RuleJson: match, Target: target, Prepend: true})
		} else {
			_, err = a.d.AlterRouting(ctx, &pb.AlterRoutingRequest{RuleTag: original, NewRuleTag: &name, RuleJson: &match, Target: &target})
		}
		a.post(func() {
			st.busy = false
			if err != nil {
				st.err = humanizeRPCError(err)
				return
			}
			disabled := false
			if r := a.ruleByTag(original); r != nil {
				disabled = r.Disabled
			}
			updated := &pb.RoutingRule{RuleTag: name, RuleJson: match, Target: target, Disabled: disabled}
			if original == "" {
				a.rules.rules = append([]*pb.RoutingRule{updated}, a.rules.rules...)
			} else {
				for i, r := range a.rules.rules {
					if r.RuleTag == original {
						a.rules.rules[i] = updated
						break
					}
				}
			}
			a.indexRules()
			a.prefetchAppIcons()
			a.prefetchFavicons()
			a.goBack()
			if a.path == ruleDetailsPath {
				a.rulePage = decodeRule(updated)
			}
		})
	})
}

// Keep a separate function for deletion so failures leave the review page intact.
func (a *app) deleteSelectedRule() {
	st := &a.rulePage
	if st.busy {
		return
	}
	tag := st.original
	st.busy = true
	a.async(func() {
		ctx, cancel := rpc()
		defer cancel()
		_, err := a.d.RemoveRouting(ctx, &pb.RemoveRoutingRequest{RuleTag: tag})
		a.post(func() {
			st.busy = false
			if err != nil {
				st.err = humanizeRPCError(err)
				return
			}
			for i, r := range a.rules.rules {
				if r.RuleTag == tag {
					a.rules.rules = append(a.rules.rules[:i], a.rules.rules[i+1:]...)
					break
				}
			}
			a.indexRules()
			delete(a.swipes, tag)
			a.goBack()
			// Editing may have been opened from this rule's details.
			if a.path == ruleDetailsPath {
				a.goBack()
			}
		})
	})
}

func conditionSummary(key, value string) string {
	entries := ruleEntries(key, value)
	if len(entries) == 0 {
		return tr("Any")
	}
	first := entries[0]
	if key == "domain" {
		first = strings.TrimPrefix(strings.TrimPrefix(first, "domain:"), "full:")
		if code, ok := strings.CutPrefix(first, "geosite:"); ok {
			first, _ = geoWebsiteTitle(code)
		}
	}
	if key == "process" {
		first = fallbackName(first)
	}
	if len(entries) > 1 {
		return fmt.Sprintf("%s +%d", first, len(entries)-1)
	}
	return first
}
func (a *app) ruleConditionsCard(c *ui.Context, p palette, editing bool, st *rulePageState) {
	if !editing {
		cardBox(c, p, func() {
			for i, d := range ruleConditions {
				if i >= 5 && !st.more && st.values[d.key] == "" {
					continue
				}
				if i > 0 {
					cardDivider(c, p)
				}
				subscriptionInfoRow(c, p, d.icon, tr(d.title), conditionSummary(d.key, st.values[d.key]))
			}
		})
		return
	}
	acc := ui.AccordionBase(c)
	acc.Root.Label(tr("Rule conditions")).Radius(cardRadius).Background(p.card).Border(1, p.borderDefault).Clip().Shrink(0).Children(func() {
		for i, d := range ruleConditions {
			if i >= 5 && !st.more && st.values[d.key] == "" {
				continue
			}
			if i > 0 {
				cardDivider(c, p)
			}
			ui.Column(c.Key(d.key)).Shrink(0).Children(func() {
				open := st.expanded == d.key
				item := acc.Item(&open)
				row := item.Trigger.Label(tr(d.title)).Disabled(st.busy).Height(40).Padding(0, 10).Gap(8).AlignItems(ui.Center).Cursor(ui.CursorPointer)
				if row.Hovered() {
					row.Background(p.cardHover)
				}
				row.Children(func() {
					ui.Icon(c, d.icon).FontSize(14).TextColor(p.muted).Shrink(0)
					title := tr(d.title)
					switch d.key {
					case "domain":
						title = tr("Websites")
					case "ip":
						title = tr("IP ranges")
					case "port":
						title = tr("Ports")
					case "network":
						title = tr("Network")
					case "protocol":
						title = tr("Protocols")
					case "source":
						title = tr("Source IPs")
					case "sourcePort":
						title = tr("Source ports")
					}
					label(c, title, sizeSubhead, 16, 500, p.text).Shrink(0)
					ui.Row(c).Grow(1).Basis(0).MinWidth(0).Gap(4).AlignItems(ui.Center).Justify(ui.End).Clip().Children(func() { a.ruleSelectionSummary(c, p, d.key, st.values[d.key]) })

					ui.Icon(c, iconChevronRight).FontSize(12).TextColor(p.tertiary).Rotate(90 * item.Progress())
				})
				if row.Changed() && !st.busy {
					a.toggleRuleAccordion(d.key)
				}
				item.Panel(func() {
					panelTheme := ruleFormTheme(c, p, true)
					ui.Themed(c, &panelTheme).Label(tr(d.title) + tr(" options")).Gap(8).Shrink(0).Children(func() {
						if d.key == "process" || d.key == "domain" || d.key == "ip" {
							a.ruleConditionEditor(c, p, d)
						} else {
							ui.Column(c).Padding(10).Gap(8).Children(func() { a.ruleConditionEditor(c, p, d) })
						}
					})
				})
			})
		}
	})
}
func (a *app) toggleRuleAccordion(key string) {
	st := &a.rulePage
	if strings.TrimSpace(st.entry) != "" {
		a.addRuleConditionEntry(st.expanded)
		if st.entry != "" {
			return
		}
	}
	if st.expanded == key {
		st.expanded = ""
	} else {
		st.expanded = key
	}
	st.entry, st.err = "", ""
	st.revealTarget = st.expanded == "destination"
	st.targetRevealSettled = false
}

func (a *app) ruleEditView(c *ui.Context, p palette) {
	st := &a.rulePage
	title := tr("New Rule")
	if st.original != "" {
		title = tr("Edit Rule")
	}
	formTheme := ruleFormTheme(c, p, false)
	ui.Themed(c, &formTheme).Background(ui.Transparent).Fill().Children(func() {
		ui.Column(c).Bind(&st.editHeader).Padding(pageInset, pageInset, 0).Shrink(0).Children(func() {
			a.pageHeader(c, p, title)
		})
		formScroll := ui.Scroll(c).Label(tr("Rule form")).TrackScroll(&st.formScroll).Grow(1).Basis(0).MinHeight(0).Padding(0, pageInset, 8).Children(func() {
			ui.Column(c).Bind(&st.editContent).Gap(10).Shrink(0).Children(func() {
				titled(c, p, tr("MATCH WHEN"), nil, func() { a.ruleConditionsCard(c, p, true, st) })
				moreTitle := tr("+ more conditions")
				if st.more {
					moreTitle = tr("- less conditions")
				}
				more := ui.ButtonBase(c.Key("more-conditions")).Label(moreTitle).AlignSelf(ui.Start).Padding(3, 0).Cursor(ui.CursorPointer).Disabled(st.busy)
				more.Children(func() { label(c, moreTitle, sizeCaption, 16, 400, p.muted) })
				if more.Clicked() {
					st.more = !st.more
					if !st.more && (st.expanded == "protocol" || st.expanded == "source" || st.expanded == "sourcePort") {
						st.expanded = ""
					}
				}
				label(c, tr("All specified conditions must match. Entries within a condition match any."), sizeMicro, 15, 400, p.muted).Label(tr("Rule matching explanation"))
				if len(st.extra) > 0 {
					label(c, tr("Additional advanced conditions are preserved."), sizeMicro, 14, 400, p.muted)
				}
				a.ruleDestinationAccordion(c, p)

			})
		})
		if st.revealTarget {
			st.formScroll.Y = math.MaxFloat32
			formScroll.TrackScroll(&st.formScroll)
			if st.targetRevealSettled {
				st.revealTarget = false
				st.targetRevealSettled = false
			}
		}

		ui.Column(c).Bind(&st.editFooter).Padding(8, pageInset, pageInset).Gap(8).Shrink(0).Children(func() {
			field(c, p, "rule-name", tr("Rule name"), &st.name, tr("e.g. Work apps"), iconTag, p.muted, nil)
			if st.err != "" {
				label(c, st.err, sizeCaption, 15, 400, p.errorC).MaxLines(3)
			}
			ui.Row(c).Gap(8).Children(func() {
				ui.Row(c).Grow(1).Basis(0).Gap(8).Children(func() {
					if st.removing {
						remove := ui.Button(c.Key("confirm-editor-remove"), tr("Remove")).Label(tr("Confirm remove rule")).Height(28).Grow(1).Basis(0).
							Disabled(st.busy).Background(p.errorSurface).Border(1, p.errorBorder).TextColor(p.errorC)
						if remove.Clicked() {
							st.removing = false
							a.deleteSelectedRule()
						}
						return
					}
					if st.original != "" {
						trash := ui.Button(c.Key("delete-rule"), "").Label(tr("Delete rule")).Tooltip(tr("Delete rule")).
							Size(28, 28).Padding(0).Shrink(0).Disabled(st.busy)
						trash.Children(func() { ui.Icon(c, iconTrash).FontSize(15).TextColor(p.muted) })
						if trash.Clicked() {
							st.removing = true
						}
					}
					if ui.Button(c.Key("cancel-rule"), tr("Cancel")).Label(tr("Cancel rule editing")).Height(28).Grow(1).Basis(0).Disabled(st.busy).Clicked() {
						a.goBack()
					}
				})
				ui.Column(c).Grow(1).Basis(0).Children(func() {
					if ui.PrimaryButton(c.Key("save-rule"), tr("Save rule")).Label(tr("Save rule changes")).Height(28).Disabled(st.busy).Clicked() {
						a.saveRule()
					}
				})
			})
		})
	})
	header, content, footer := st.editHeader.Bounds(c), st.editContent.Bounds(c), st.editFooter.Bounds(c)
	if header.H > 0 && content.H > 0 && footer.H > 0 {
		measured := min(pageOf(ruleEditPath).height, float32(math.Ceil(float64(header.H+content.H+8+footer.H+2*panelBorderWidth(c)))))
		if abs32(st.editHeight-measured) > 0.5 {
			st.editHeight = measured
			c.AnimationFrame()
		}
		if a.path == ruleEditPath {
			a.toHeight = st.editHeight
		}
	} else {
		c.AnimationFrame()
	}
	if a.path == ruleEditPath {
		a.settleHeight()
	}
}
func (a *app) ruleDetailsView(c *ui.Context, p palette) {
	st := &a.rulePage
	r := a.ruleByTag(st.original)
	ui.Column(c).Fill().Children(func() {
		ui.Column(c).Padding(pageInset, pageInset, 0).Shrink(0).Children(func() {
			a.pageHeader(c, p, tr("Rule"), func() {
				if r != nil && iconButton(c, "edit-rule", iconPencil, tr("Edit rule"), p, iconSM, variantGhost, 15) && !st.busy {
					a.editRule(r)
				}
			})
		})
		if r == nil {
			label(c, tr("This rule is no longer available."), sizeBody, 18, 400, p.muted).Padding(pageInset)
			return
		}
		ui.Scroll(c).Grow(1).Basis(0).MinHeight(0).Padding(0, pageInset, 8).Children(func() {
			ui.Column(c).Gap(10).Shrink(0).Children(func() {
				label(c, r.RuleTag, sizeTitle, 20, 600, p.text)
				ui.Row(c).AlignItems(ui.Center).Justify(ui.SpaceBetween).Children(func() {
					a.destinationBadge(c, p, r.Target)
					enabled := !r.Disabled
					if pending := a.pend.ruleEnabled[r.RuleTag]; pending != nil {
						if on, ok := pending.get(); ok {
							enabled = on
						}
					}
					if next, changed := nativeSwitch(c, "rule-enabled", tr("Enable rule"), enabled, switchSubdued, false, st.busy, p); changed {
						a.setRuleEnabled(r.RuleTag, next)
					}
				})
				if r.DisabledReason != "" {
					label(c, r.DisabledReason, sizeCaption, 15, 400, p.errorC)
				}
				titled(c, p, tr("MATCH WHEN"), nil, func() { a.ruleConditionsCard(c, p, false, &a.ruleReview) })
				label(c, tr("All specified conditions must match."), sizeMicro, 15, 400, p.muted)
				if len(a.ruleReview.extra) > 0 {
					data, _ := json.MarshalIndent(a.ruleReview.extra, "", "  ")
					label(c, string(data), sizeCaption, 16, 400, p.muted).Selectable()
				}
			})
		})
		ui.Column(c).Padding(8, pageInset, pageInset).Gap(8).Shrink(0).Children(func() {
			if st.err != "" {
				label(c, st.err, sizeCaption, 15, 400, p.errorC)
			}
			if !st.removing {
				if button(c, "remove-rule", tr("Remove rule"), false, st.busy, false, p) {
					st.removing = true
				}
			} else {
				label(c, tr("Remove this rule?"), sizeCaption, 15, 500, p.errorC)
				if button(c, "confirm-remove-rule", tr("Remove"), true, st.busy, st.busy, p) {
					a.deleteSelectedRule()
				}
				if thinButton(c, "keep-rule", tr("Keep rule"), false, st.busy, false, p) {
					st.removing = false
				}
			}
		})
	})
}

func (a *app) ruleSelectionSummary(c *ui.Context, p palette, key, value string) {
	entries := ruleEntries(key, value)
	if len(entries) == 0 {
		label(c, tr("Any"), sizeCaption, 16, 400, p.muted).SingleLine()
		return
	}
	if key == "process" {
		var icons []*ui.Bitmap
		for _, path := range entries[:min(3, len(entries))] {
			if !filepath.IsAbs(path) {
				break
			}
			meta := a.apps.lookup(strings.TrimSuffix(path, "/"), a.async, func() { a.post(func() {}) })
			_, bitmap := a.apps.read(meta)
			if bitmap == nil {
				break
			}
			icons = append(icons, bitmap)
		}
		if len(icons) > 0 {
			ui.Box(c).Label(tr("Selected application icons")).Size(24+float32(len(icons)-1)*14, 24).Shrink(0).Children(func() {
				for i, bitmap := range icons {
					ui.Box(c.Key(i)).Label(fmt.Sprintf(tr("Selected application %d"), i+1)).Absolute().Left(float32(i)*14).Top(0).Size(24, 24).Radius(6).Background(p.card).Border(1, p.borderDefault).Clip().Center().Children(func() { ui.Image(c, bitmap).Size(24, 24).MarginY(-3) })
				}
			})
			if extra := len(entries) - len(icons); extra > 0 {
				label(c, fmt.Sprintf("+%d", extra), sizeMicro, 16, 500, p.muted).Shrink(0)
			}
			return
		}
	}
	title := conditionSummary(key, entries[0])
	if key == "network" || key == "protocol" {
		title = strings.ToUpper(title)
	}
	if key == "ip" && strings.HasPrefix(entries[0], "geoip:") {
		if name, _, _, ok := humanizeGeoIP(entries[0]); ok {
			title = name
		}
	}
	label(c, title, sizeCaption, 16, 400, p.muted).SingleLine().MinWidth(0).MaxWidth(112).Shrink(1)
	if len(entries) > 1 {
		label(c, fmt.Sprintf("+%d", len(entries)-1), sizeMicro, 16, 500, p.muted).Shrink(0)
	}
}
