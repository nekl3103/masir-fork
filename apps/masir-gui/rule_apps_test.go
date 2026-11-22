package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

func TestInstalledRuleAppsNeverIncludeRunningServices(t *testing.T) {
	installed := []ruleApp{{Name: "Safari", Path: "/Applications/Safari.app/"}, {Name: "Terminal", Path: "/System/Applications/Utilities/Terminal.app/"}}
	running := []ruleApp{{Name: "appleaccountd", Path: "/usr/libexec/appleaccountd"}, {Name: "Safari Web Content", Path: "/Applications/Safari.app/Contents/Frameworks/WebKit.app/Contents/MacOS/WebKit"}}
	got := mergeInstalledRuleApps(installed, running, "darwin")
	if len(got) != 2 || got[0].Name != "Safari" || !got[0].Running || got[1].Running {
		t.Fatalf("wrong installed catalog: %+v", got)
	}
	windows := mergeInstalledRuleApps([]ruleApp{{Name: "Browser", Path: `C:\Program Files\Browser\Browser.exe`}, {Name: "Duplicate", Path: `c:\program files\browser\browser.exe`}}, []ruleApp{{Path: `c:\PROGRAM FILES\Browser\Browser.exe`}, {Path: `C:\Windows\System32\svchost.exe`}}, "windows")
	if len(windows) != 1 || !windows[0].Running {
		t.Fatalf("windows identity: %+v", windows)
	}
}
func appPickerTestFixture(a *app) {
	a.ruleApps = ruleAppState{selected: map[string]bool{}, lastTab: -1, all: []ruleApp{
		{Name: "Browser", Path: "/Applications/Browser.app/", Icon: "/Applications/Browser.app", Running: true},
		{Name: "Notes", Path: "/Applications/Notes.app/", Icon: "/Applications/Notes.app"},
	}}
	a.rulePage.expanded = "process"
	a.filterRuleApps()
}
func TestRuleAppSelectionAcrossTabsSearchAndManual(t *testing.T) {
	a := newTestApp(ukServers())
	a.newRule()
	appPickerTestFixture(a)
	tt := tester(a)
	tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	tt.SetSize(panelWidth, 620)
	tt.Frame()
	tt.Frame()
	if _, ok := tt.Find("Application paths"); ok {
		t.Fatal("manual input shown by default")
	}
	if err := tt.Click("Select Browser"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.rulePage.values["process"] != "/Applications/Browser.app/" || len(a.ruleApps.filtered) != 2 {
		t.Fatal("selection changed the installed catalog")
	}
	if err := tt.Click("Apps"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Frame()
	if len(a.ruleApps.chosen) != 1 || a.ruleApps.chosen[0].Name != "Browser" {
		t.Fatal("selected filter failed")
	}
	if err := tt.Click("Select Browser"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Frame()
	if len(a.ruleApps.chosen) != 0 || a.rulePage.values["process"] != "" {
		t.Fatal("deselection failed")
	}
	if err := tt.Click("Manual"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Frame()
	if err := tt.Click("Application paths"); err != nil {
		t.Fatal(err)
	}
	tt.Type("/Applications/Notes.app/\nmy-worker")
	tt.Frame()
	if err := tt.Click("Apps"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Frame()
	if len(a.ruleApps.chosen) != 2 {
		t.Fatalf("manual choices not retained: %+v", a.ruleApps.filtered)
	}
	if err := tt.Click("Apps"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Frame()
	if len(a.ruleApps.filtered) != 2 {
		t.Fatal("custom process leaked into installed choices")
	}
	if err := tt.Click("Search applications…"); err != nil {
		t.Fatal(err)
	}
	tt.Type("notes")
	tt.Frame()
	tt.Frame()
	if len(a.ruleApps.filtered) != 1 || a.ruleApps.filtered[0].Name != "Notes" {
		t.Fatal("search failed")
	}
	if err := tt.Click("Clear search"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Frame()
	if len(a.ruleApps.filtered) != 2 {
		t.Fatal("clearing search failed")
	}
	if _, ok := tt.Find("Application group Selected"); !ok {
		t.Fatal("missing selected group")
	}
	if _, ok := tt.Find("Running"); ok {
		t.Fatal("running filter still present")
	}

	if !strings.Contains(a.rulePage.values["process"], "my-worker") {
		t.Fatal("filtering dropped a manual matcher")
	}
}
func TestPickedRulePathsStayOutOfInstalledCatalog(t *testing.T) {
	a := newTestApp(ukServers())
	a.newRule()
	appPickerTestFixture(a)
	a.addPickedRuleApps([]string{"/Tools/My Worker"}, true)
	a.filterRuleApps()
	if a.ruleApps.tab != ruleAppsAll || a.rulePage.values["process"] != "/Tools/My Worker/" || len(a.ruleApps.chosen) != 1 {
		t.Fatal("picked folder not selected")
	}
	a.ruleApps.tab = ruleAppsAll
	a.filterRuleApps()
	if len(a.ruleApps.filtered) != 2 {
		t.Fatal("picked folder became an installed application")
	}
}

func TestRulePickerRejectsPathsWithLineBreaks(t *testing.T) {
	for _, tc := range []struct {
		name, invalid string
		directory     bool
	}{
		{"folder with LF", "\n", true},
		{"application with CR", "\r", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := newTestApp(ukServers())
			a.newRule()
			appPickerTestFixture(a)
			const existing = "/Applications/Browser.app/"
			a.ruleApps.selected[existing] = true
			a.applyRuleApps()
			before, err := a.rulePage.matchJSON()
			if err != nil {
				t.Fatal(err)
			}
			const valid = "/Tools/Notes"
			a.openRuleDialog = func(mygo.OpenDialogOptions) ([]string, error) {
				return []string{valid, tc.invalid}, nil
			}
			a.pickRuleApp(tc.directory)
			if a.ruleApps.err == "" || len(a.ruleApps.selected) != 1 || !a.ruleApps.selected[existing] || len(a.ruleApps.custom) != 0 {
				t.Fatal("invalid selection did not leave the existing selection intact with an error")
			}
			if after, err := a.rulePage.matchJSON(); err != nil || after != before {
				t.Fatalf("invalid selection changed the rule JSON: %s (%v)", after, err)
			}
			a.openRuleDialog = func(mygo.OpenDialogOptions) ([]string, error) {
				return []string{valid}, nil
			}
			a.pickRuleApp(tc.directory)
			want := valid
			if tc.directory {
				want += "/"
			}
			if a.ruleApps.err != "" || !a.ruleApps.selected[want] {
				t.Fatal("valid retry did not clear the error and add the selected path")
			}
		})
	}
}

func TestInstalledRuleAppSelectionRejectsLineBreaks(t *testing.T) {
	a := newTestApp(ukServers())
	a.newRule()
	appPickerTestFixture(a)
	a.ruleApps.all[0].Path += "\n"
	a.filterRuleApps()
	tt := tester(a)
	tt.Frame()
	tt.Frame()
	if err := tt.Click("Select Browser"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.ruleApps.err == "" || len(a.ruleApps.selected) != 0 || a.rulePage.values["process"] != "" {
		t.Fatal("installed path with a line break entered the rule")
	}
}

func TestRuleAccordionContentIsFullWidth(t *testing.T) {
	for _, key := range []string{"process", "domain", "destination"} {
		t.Run(key, func(t *testing.T) {
			a := newTestApp(ukServers())
			a.newRule()
			appPickerTestFixture(a)
			a.rulePage.expanded = key
			tt := tester(a)
			tt.SetPreferences(ui.Preferences{ReduceMotion: true})
			tt.SetSize(panelWidth, 620)
			tt.Frame()
			tt.Frame()
			cardName, panelName := "Rule conditions", "Websites and domains options"
			if key == "process" {
				panelName = "Applications options"
			}
			if key == "destination" {
				cardName, panelName = "Rule destination", "Destination options"
			}
			card, ok := tt.Find(cardName)
			if !ok {
				t.Fatal("missing card")
			}
			panel, ok := tt.Find(panelName)
			if !ok {
				t.Fatal("missing panel")
			}
			if abs32(panel.X-card.X-1) > 0.5 || abs32(panel.W-card.W+2) > 0.5 {
				t.Fatalf("inset content: card=%+v panel=%+v", card, panel)
			}
		})
	}
}

func TestRuleAppRowSelectionChangesBorderAndBackground(t *testing.T) {
	for _, dark := range []bool{false, true} {
		t.Run(map[bool]string{false: "light", true: "dark"}[dark], func(t *testing.T) {
			a := newTestApp(ukServers())
			a.newRule()
			appPickerTestFixture(a)
			tt := tester(a)
			tt.SetDark(dark)
			tt.SetPreferences(ui.Preferences{ReduceMotion: true})
			tt.SetSize(panelWidth, 620)
			tt.Frame()
			tt.Frame()
			row, ok := tt.Find("Select Browser")
			if !ok {
				t.Fatal("missing row")
			}
			sample := func(x, y float32) [3]uint32 {
				r, g, b, _ := tt.Image().At(int(x), int(y)).RGBA()
				return [3]uint32{r, g, b}
			}
			beforeBorder := sample(row.X, row.Y+row.H/2)
			beforeFill := sample(row.X+row.W-22, row.Y+row.H-4)
			if err := tt.Click("Select Browser"); err != nil {
				t.Fatal(err)
			}
			tt.Frame()
			tt.Frame()
			afterBorder := sample(row.X, row.Y+row.H/2)
			afterFill := sample(row.X+row.W-22, row.Y+row.H-4)
			if beforeBorder == afterBorder || beforeFill == afterFill {
				t.Fatalf("selection is not visually distinct: border %v -> %v, fill %v -> %v", beforeBorder, afterBorder, beforeFill, afterFill)
			}
		})
	}
}
func TestRuleAppLongNamesStayInsideRows(t *testing.T) {
	a := newTestApp(ukServers())
	a.newRule()
	appPickerTestFixture(a)
	a.ruleApps.all[0].Name = strings.Repeat("Very Long Application ", 12)
	a.filterRuleApps()
	tt := tester(a)
	tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	tt.SetSize(panelWidth, 620)
	tt.Frame()
	tt.Frame()
	running, ok := tt.Find("Application is running")
	if !ok {
		t.Fatal("missing running state")
	}
	list, ok := tt.Find("Application choices")
	if !ok {
		t.Fatal("missing choices")
	}
	if running.X+running.W > list.X+list.W || running.X < list.X || running.Y < list.Y || running.Y+running.H > list.Y+list.H {
		t.Fatalf("running state escaped list: status=%+v list=%+v", running, list)
	}
}
func TestRuleAppMatchesSurviveSaveAndReopen(t *testing.T) {
	f := &ruleEditorDaemon{fakeDaemon: ukServers()}
	a := newTestApp(f.fakeDaemon)
	a.d = f
	a.newRule()
	appPickerTestFixture(a)
	a.ruleApps.selected["/Applications/Browser.app/"] = true
	a.applyRuleApps()
	a.addPickedRuleApps([]string{"/Tools/Worker"}, true)
	a.rulePage.name = "Work apps"
	a.saveRule()
	if f.add == nil {
		t.Fatalf("not saved: %s", a.rulePage.err)
	}
	r := a.ruleByTag("Work apps")
	a.editRule(r)
	a.loadRuleApps()
	entries := ruleEntries("process", a.rulePage.values["process"])
	if len(entries) != 2 || !a.ruleApps.selected["/Tools/Worker/"] || !a.ruleApps.selected["/Applications/Browser.app/"] {
		t.Fatalf("lost matches: %v", entries)
	}
}
func TestStaleRuleAppDiscoveryCannotReplaceAnotherDraft(t *testing.T) {
	a := newTestApp(ukServers())
	var work []func()
	a.async = func(fn func()) { work = append(work, fn) }
	a.newRule()
	old := work[0]
	a.newRule()
	token := a.ruleApps.session
	a.rulePage.values["process"] = "my-worker"
	old()
	if a.ruleApps.session != token || !a.ruleApps.loading || a.rulePage.values["process"] != "my-worker" {
		t.Fatal("stale discovery changed the new draft")
	}
}

func TestRuleEditorPrefetchesAppsBeforeAccordionOpens(t *testing.T) {
	for _, edit := range []bool{false, true} {
		t.Run(fmt.Sprint("edit=", edit), func(t *testing.T) {
			a := newTestApp(ukServers())
			var work []func()
			a.async = func(fn func()) { work = append(work, fn) }
			if edit {
				a.editRule(a.ruleByTag("r3"))
			} else {
				a.newRule()
			}
			if !a.ruleApps.loading || len(work) != 3 || a.rulePage.expanded != "" {
				t.Fatal("opening the editor did not prefetch apps with the accordion closed")
			}
			if edit && !a.ruleApps.selected["/Applications/Spotify.app/"] {
				t.Fatal("prefetch lost the existing app selection")
			}
			work[0]()
			if a.ruleApps.loading {
				t.Fatal("prefetch did not finish before opening the accordion")
			}
			token := a.ruleApps.session
			a.toggleRuleAccordion("process")
			if len(work) != 3 {
				t.Fatal("opening Applications queued another discovery")
			}
			tt := tester(a)
			tt.Frame()
			if a.ruleApps.loading || a.ruleApps.session != token {
				t.Fatal("opening Applications restarted discovery")
			}
		})
	}
}

func TestRuleAppDialogHasOwnerAndCancelPreservesDraft(t *testing.T) {
	a := newTestApp(ukServers())
	a.newRule()
	appPickerTestFixture(a)
	a.ruleApps.selected["/Applications/Browser.app/"] = true
	a.applyRuleApps()
	owner := &mygo.Window{}
	a.pickerParent = func() *mygo.Window { return owner }
	calls := 0
	a.openRuleDialog = func(opts mygo.OpenDialogOptions) ([]string, error) {
		calls++
		if opts.Parent != owner || !opts.Multiple || !opts.Directory {
			t.Fatal("picker was not attached to its owning window")
		}
		return nil, nil
	}
	a.pickRuleApp(true)
	if calls != 1 || a.ruleApps.picking || a.path != ruleEditPath || a.rulePage.values["process"] != "/Applications/Browser.app/" {
		t.Fatal("cancelled picker lost its draft")
	}
}

func TestRuleSegmentsShareTheFullContainerWidth(t *testing.T) {
	for _, options := range [][]string{{"Apps", "Manual"}, {"Category", "Domain"}, {"Any", "TCP", "UDP"}, {"Proxy", "Direct", "Block"}} {
		t.Run(options[0], func(t *testing.T) {
			selected := 0
			tt := ui.NewTester(func(c *ui.Context) {
				ui.Column(c).Width(260).Children(func() { ruleSegments(c, "segments", &selected, paletteFor(c), options...) })
			}, 300, 80)
			tt.Frame()
			tt.Frame()
			track, ok := tt.Find("segments")
			if !ok {
				t.Fatal("missing track")
			}
			var width float32
			for i, title := range options {
				r, ok := tt.Find(title)
				if !ok {
					t.Fatal(title)
				}
				if i == 0 {
					width = r.W
				} else if abs32(width-r.W) > 0.5 {
					t.Fatalf("unequal segment widths: %v vs %v", width, r.W)
				}
				if i == len(options)-1 && track.X+track.W-r.X-r.W > 4 {
					t.Fatal("unfilled track after last trigger")
				}
			}
		})
	}
}
func TestApplicationRowsHidePathsOutsideManual(t *testing.T) {
	a := newTestApp(ukServers())
	a.newRule()
	appPickerTestFixture(a)
	tt := tester(a)
	tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	tt.SetSize(panelWidth, 620)
	tt.Frame()
	tt.Frame()
	if tt.HasText("/Applications/Browser.app/") {
		t.Fatal("installed path displayed in the app list")
	}
	if err := tt.Click("Select Browser"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.rulePage.values["process"] != "/Applications/Browser.app/" {
		t.Fatal("hiding the path changed its matcher")
	}
	if err := tt.Click("Manual"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Frame()
	if _, ok := tt.Find("Application paths"); !ok {
		t.Fatal("manual paths unavailable")
	}
}

func TestApplicationTabsFillTheirAccordion(t *testing.T) {
	a := newTestApp(ukServers())
	a.newRule()
	appPickerTestFixture(a)
	tt := tester(a)
	tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	tt.SetSize(panelWidth, 620)
	tt.Frame()
	tt.Frame()
	panel, _ := tt.Find("Applications options")
	track, ok := tt.Find("Application source")
	if !ok {
		t.Fatal("missing tabs")
	}
	if abs32(track.W-panel.W+16) > 0.5 {
		t.Fatalf("tabs did not fill their padded container: track=%+v panel=%+v", track, panel)
	}
	var width float32
	for i, name := range []string{"Apps", "Manual"} {
		r, _ := tt.Find(name)
		if i == 0 {
			width = r.W
		} else if abs32(width-r.W) > 0.5 {
			t.Fatalf("unequal tab width for %s: %v vs %v", name, r.W, width)
		}
	}
}
