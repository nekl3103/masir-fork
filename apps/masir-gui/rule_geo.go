package main

import (
	"fmt"
	"slices"
	"strings"

	pb "github.com/Gozargah/Masir/libs/masir-proto/gen/masir/daemon/v1"
	"github.com/egoist/mygo/ui"
)

type ruleGeoOption struct {
	code, title, titleKey, flag, search, count string
	entries                                    uint32
}

type ruleGeoState struct {
	all, filtered                    []ruleGeoOption
	known                            map[string]bool
	selected                         map[string]bool
	query, lastQuery, lastMatch, err string
	loading, loaded, dirty           bool
	localeJob                        uint64
	list                             ui.ListState
}

func geoWebsiteTitle(code string) (string, string) {
	title, flag, _, ok := humanizeGeosite("geosite:" + code)
	if !ok {
		title = formatTopic(strings.TrimPrefix(code, "category-"))
	}
	return title, flag
}

func geoWebsiteOption(code string, count uint32) ruleGeoOption {
	title, flag := geoWebsiteTitle(code)
	v := ruleGeoOption{code: code, title: title, flag: flag, entries: count}
	v.titleKey = strings.ToLower(title)
	v.search = strings.ToLower(title + " " + code + " geosite:" + code)
	if count > 0 {
		if count > 1 {
			v.count = fmt.Sprintf(tr("%d domains"), count)
		} else {
			v.count = tr("1 domain")
		}
	}
	return v
}

// Website and IP catalogs share selection, search and list rendering, while
// keeping independent queries and asynchronous cache state.
type ruleGeoCatalog struct {
	key, prefix, title, noun, search string
	kind                             pb.GeoKind
}

func geoCatalog(key string) ruleGeoCatalog {
	if key == "ip" {
		return ruleGeoCatalog{"ip", "geoip:", tr("GeoIP list"), tr("GeoIP lists"), tr("Search country or GeoIP category…"), pb.GeoKind_GEO_KIND_IP}
	}
	return ruleGeoCatalog{"domain", "geosite:", tr("Website list"), tr("website lists"), tr("Search category or website…"), pb.GeoKind_GEO_KIND_SITE}
}

func (a *app) geoState(key string) *ruleGeoState {
	if key == "ip" {
		return &a.ruleGeoIP
	}
	return &a.ruleGeo
}

func geoCatalogOption(key, code string, count uint32) ruleGeoOption {
	if key != "ip" {
		return geoWebsiteOption(code, count)
	}
	title, flag, _, ok := humanizeGeoIP("geoip:" + code)
	if !ok {
		title = formatTopic(code)
	}
	v := ruleGeoOption{code: code, title: title, flag: flag, entries: count, titleKey: strings.ToLower(title)}
	v.search = strings.ToLower(title + " " + code + " geoip:" + code)
	if count == 1 {
		v.count = tr("1 range")
	} else if count > 1 {
		v.count = fmt.Sprintf(tr("%d ranges"), count)
	}
	return v
}

// Cache metadata across editors, but revalidate on opening: the daemon already
// indexes installed files by modification time and size. No geo data enters a frame.
func (a *app) prepareRuleGeo() {
	for _, key := range []string{"domain", "ip"} {
		st := a.geoState(key)
		st.query, st.dirty = "", true
		a.loadRuleGeoFor(key)
	}
}

func (a *app) loadRuleGeo() { a.loadRuleGeoFor("domain") }

func (a *app) loadRuleGeoFor(key string) {
	catalog := geoCatalog(key)
	st := a.geoState(key)
	if st.loading {
		return
	}
	st.loading, st.err = true, ""
	d := a.d
	lang := appLanguage()
	a.async(func() {
		ctx, cancel := rpc()
		defer cancel()
		res, err := d.ListGeoCategories(ctx, &pb.ListGeoCategoriesRequest{Kind: catalog.kind})
		var all []ruleGeoOption
		known := map[string]bool{}
		if err == nil && res != nil {
			for _, cat := range res.Categories {
				code := strings.ToLower(strings.TrimSpace(cat.Code))
				if code == "" || known[code] {
					continue
				}
				known[code] = true
				all = append(all, geoCatalogOption(key, code, cat.Entries))
			}
			sortGeoOptions(all)
		}
		a.post(func() {
			st.loading = false
			if err != nil {
				st.err = tr("Couldn't load ") + catalog.noun + tr(". Try refreshing the list.")
				return
			}
			st.localeJob++
			st.all, st.known, st.loaded, st.dirty = all, known, true, true
			if lang != appLanguage() {
				a.localizeRuleGeo(key)
			}
		})
	})
}

func sortGeoOptions(options []ruleGeoOption) {
	slices.SortFunc(options, func(a, b ruleGeoOption) int {
		if title := strings.Compare(a.titleKey, b.titleKey); title != 0 {
			return title
		}
		return strings.Compare(a.code, b.code)
	})
}

func (a *app) localizeRuleGeo(key string) {
	st := a.geoState(key)
	st.localeJob++
	job, source := st.localeJob, st.all
	st.dirty = true
	if len(source) == 0 {
		return
	}
	a.async(func() {
		localized := make([]ruleGeoOption, len(source))
		for i, option := range source {
			localized[i] = geoCatalogOption(key, option.code, option.entries)
		}
		sortGeoOptions(localized)
		a.post(func() {
			if st.localeJob == job {
				st.all, st.dirty = localized, true
			}
		})
	})
}

// Only recompute on a query, selection or catalog change, never on a paint frame.
func (a *app) filterRuleGeo() { a.filterRuleGeoFor("domain") }

func (a *app) filterRuleGeoFor(key string) {
	catalog := geoCatalog(key)
	st := a.geoState(key)
	match := a.rulePage.values[key]
	if !st.dirty && st.query == st.lastQuery && st.lastMatch == match {
		return
	}
	st.selected = map[string]bool{}
	var selectedCodes []string
	for _, entry := range ruleEntries(key, match) {
		if code, ok := strings.CutPrefix(entry, catalog.prefix); ok {
			st.selected[code] = true
			selectedCodes = append(selectedCodes, code)
		}
	}
	q := strings.ToLower(strings.TrimSpace(st.query))
	q = strings.TrimPrefix(q, catalog.prefix)
	st.filtered = st.filtered[:0]
	add := func(v ruleGeoOption) {
		if strings.Contains(v.search, q) {
			st.filtered = append(st.filtered, v)
		}
	}
	for _, v := range st.all {
		add(v)
	}
	// Old or manually entered categories remain removable even after a geo update.
	for _, code := range selectedCodes {
		if !st.known[code] {
			add(geoCatalogOption(key, code, 0))
		}
	}
	rank := func(v ruleGeoOption) int {
		title := v.titleKey
		if v.code == q || title == q {
			return 0
		}
		if strings.HasPrefix(v.code, q) || strings.HasPrefix(title, q) {
			return 1
		}
		return 2
	}
	slices.SortStableFunc(st.filtered, func(a, b ruleGeoOption) int { return rank(a) - rank(b) })
	if st.query != st.lastQuery {
		st.list.ScrollTo(0, ui.Start)
	}
	st.lastQuery, st.lastMatch, st.dirty = st.query, match, false
	st.list.Key = func(i int) any { return st.filtered[i].code }
	st.list.Label = func(i int) string { return st.filtered[i].title }
}

func (a *app) ruleGeoEditor(c *ui.Context, p palette, d ruleCondition) {
	catalog := geoCatalog(d.key)
	st := a.geoState(d.key)
	panelBar(c, p, func() { searchInput(c, "geo-search", &st.query, catalog.search, p) }, func() {
		if iconButton(c, "retry-geo", iconRefresh, tr("Refresh ")+catalog.noun, p, iconMD, variantSubtle, 12) && !st.loading {
			a.loadRuleGeoFor(d.key)
		}
	})
	a.filterRuleGeoFor(d.key)
	ui.Column(c).Label(catalog.title + tr(" results")).Height(208).Shrink(0).Children(func() {
		if len(st.filtered) > 0 {
			containListScroll(ui.List(c, &st.list, len(st.filtered), func(i int) { a.ruleGeoRow(c, p, d.key, st.filtered[i]) })).Label(catalog.title+tr(" choices")).Grow(1).Basis(0).MinHeight(0).Padding(4, 6).Gap(3).ScrollbarInsets(0, 1, 0)
			return
		}
		ui.Column(c).Grow(1).Padding(12).Gap(6).Justify(ui.Center).AlignItems(ui.Center).Children(func() {
			message := tr("No ") + catalog.noun + tr(" installed. Update geo data in Settings.")
			switch {
			case st.loading && !st.loaded:
				ui.Spinner(c).Size(14, 14)
				message = tr("Loading ") + catalog.noun + "…"
			case st.err != "":
				message = st.err
			case st.query != "":
				message = tr("No matching ") + catalog.noun + "."
			}
			label(c, message, sizeCaption, 16, 400, p.muted).TextAlign(ui.Center)
		})
	})
	ui.Row(c).Padding(6, 10).Gap(6).Justify(ui.SpaceBetween).Children(func() {
		label(c, fmt.Sprintf(tr("%d selected"), len(st.selected)), sizeMicro, 15, 400, p.muted)
		label(c, fmt.Sprintf(tr("%d lists"), len(st.all)), sizeMicro, 15, 400, p.tertiary)
	})
	if st.err != "" && len(st.filtered) > 0 {
		label(c, st.err, sizeCaption, 16, 400, p.errorC).Padding(0, 10, 8)
	}
}

func (a *app) ruleGeoRow(c *ui.Context, p palette, key string, v ruleGeoOption) {
	catalog := geoCatalog(key)
	st := a.geoState(key)
	selected := st.selected[v.code]
	row := ui.ToggleBase(c.Key(v.code), &selected).Label(tr("Select ")+strings.ToLower(catalog.title)+" "+v.code).Disabled(a.rulePage.busy).Padding(6).Gap(8).Radius(6).AlignItems(ui.Center).Border(1, ui.Transparent).Cursor(ui.CursorPointer)
	if selected {
		row.Background(p.accentSurface).Border(1, p.borderFocus)
	} else if row.Hovered() {
		row.Background(p.cardHover)
	}
	row.Children(func() {
		iconTile(c, p, func() {
			if v.flag != "" {
				flagElement(c, v.flag, 18)
			} else {
				glyph := iconGlobe
				if key == "ip" {
					glyph = iconLayers
				}
				ui.Icon(c, glyph).FontSize(14).TextColor(p.muted)
			}
		})
		ui.Column(c).Grow(1).Basis(0).MinWidth(0).Children(func() {
			ui.Row(c).Justify(ui.SpaceBetween).Children(func() {
				label(c, v.title, sizeBody, 16, 500, p.text).Grow(1).Basis(0).MinWidth(0).SingleLine()
				label(c, v.count, sizeMicro, 14, 400, p.muted).Shrink(0).SingleLine()
			})
			code := catalog.prefix + v.code
			if st.loaded && !st.known[v.code] {
				code += tr(" · Not installed")
			}
			label(c, code, sizeMicro, 14, 400, p.muted).SingleLine()
		})
		if selected {
			ui.Icon(c, iconCheck).FontSize(12).TextColor(p.accent).Shrink(0)
		}
	})
	if row.Changed() {
		entries := ruleEntries(key, a.rulePage.values[key])
		value := catalog.prefix + v.code
		if selected {
			entries = append(entries, value)
		} else {
			entries = slices.DeleteFunc(entries, func(s string) bool { return s == value })
		}
		a.rulePage.values[key] = strings.Join(entries, "\n")
	}
}
