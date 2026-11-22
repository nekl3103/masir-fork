package main

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/egoist/mygo/ui"
	"image"
	"path/filepath"
	"strings"
	"testing"
	"time"

	pb "github.com/Gozargah/Masir/libs/masir-proto/gen/masir/daemon/v1"
	"google.golang.org/grpc"
)

type ruleEditorDaemon struct {
	*fakeDaemon
	add   *pb.AddRoutingRequest
	alter *pb.AlterRoutingRequest
	err   error
}

func (f *ruleEditorDaemon) AddRouting(_ context.Context, r *pb.AddRoutingRequest, _ ...grpc.CallOption) (*pb.AddRoutingResponse, error) {
	f.add = r
	return &pb.AddRoutingResponse{}, f.err
}
func (f *ruleEditorDaemon) AlterRouting(_ context.Context, r *pb.AlterRoutingRequest, _ ...grpc.CallOption) (*pb.AlterRoutingResponse, error) {
	f.alter = r
	return &pb.AlterRoutingResponse{}, f.err
}
func TestRuleEditorPreservesAdvancedConditions(t *testing.T) {
	st := decodeRule(&pb.RoutingRule{RuleTag: "work", Target: "direct", RuleJson: `{"domain":["domain:example.com"],"network":"tcp,udp","attrs":"attrs[':method'] == 'GET'","inboundTag":["tun"]}`})
	st.values["port"] = "443"
	raw, err := st.matchJSON()
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	json.Unmarshal([]byte(raw), &m)
	if string(m["inboundTag"]) != `["tun"]` || string(m["attrs"]) != `"attrs[':method'] == 'GET'"` {
		t.Fatalf("lost advanced matchers: %s", raw)
	}
	if string(m["network"]) != `"tcp,udp"` {
		t.Fatalf("network changed: %s", raw)
	}
}
func TestRuleValidation(t *testing.T) {
	for _, v := range []struct {
		k, v  string
		valid bool
	}{{"port", "443,1000-2000", true}, {"port", "65536", false}, {"port", "2000-1000", false}, {"ip", "2001:db8::/32", true}, {"ip", "999.1.1.1", false}, {"ip", "geoip:private", true}, {"domain", "https://example.com/path", false}, {"domain", "regexp:[", false}, {"process", "/Applications/My App.app/\nC:\\Program Files\\App\\App.exe", true}, {"protocol", "tls,quic", true}, {"protocol", "anything", false}} {
		if err := validateRuleCondition(v.k, v.v); (err == nil) != v.valid {
			t.Errorf("%s %q: %v", v.k, v.v, err)
		}
	}
	st := decodeRule(nil)
	if _, err := st.matchJSON(); err == nil {
		t.Fatal("allowed catch-all rule")
	}
}

func TestSourceIPsAcceptCommaSeparatedBatch(t *testing.T) {
	a := newTestApp(ukServers())
	a.rulePage = decodeRule(nil)
	d := ruleCondition{key: "source", title: "Source addresses", icon: iconLayers}
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Gap(8).Children(func() { a.ruleConditionEditor(c, paletteFor(c), d) })
	}, 320, 400)
	tt.Frame()
	if err := tt.Click("IP addresses or ranges"); err != nil {
		t.Fatal(err)
	}
	tt.Type("1.1.1.1, 10.0.0.0/8, 2001:db8::/32, 1.1.1.1")
	tt.KeyDown(ui.KeyEnter)
	tt.Frame()
	if got := a.rulePage.values["source"]; got != "1.1.1.1, 10.0.0.0/8, 2001:db8::/32, 1.1.1.1" || a.rulePage.entry != "" {
		t.Fatalf("batch was not added as separate unique IPs: %q (%s)", got, a.rulePage.err)
	}
	raw, err := a.rulePage.matchJSON()
	if err != nil || !strings.Contains(raw, `"source":["1.1.1.1","10.0.0.0/8","2001:db8::/32"]`) {
		t.Fatalf("source IP serialization: %s, %v", raw, err)
	}
	if _, ok := tt.Find("Add"); ok {
		t.Fatal("IP input still needs an Add button")
	}
	a.rulePage.values["source"] = "192.168.0.1, invalid-ip"
	if _, err := a.rulePage.matchJSON(); err == nil {
		t.Fatal("saved an invalid IP batch")
	}
}

func TestProtocolButtonsAllowMultipleSelections(t *testing.T) {
	for _, dark := range []bool{false, true} {
		t.Run(map[bool]string{false: "light", true: "dark"}[dark], func(t *testing.T) {
			a := newTestApp(ukServers())
			a.rulePage = decodeRule(nil)
			d := ruleCondition{key: "protocol", title: "Application protocols"}
			tt := ui.NewTester(func(c *ui.Context) {
				p := paletteFor(c)
				theme := ruleFormTheme(c, p, true)
				ui.Themed(c, &theme).Gap(8).Children(func() { a.ruleConditionEditor(c, p, d) })
			}, 320, 180)
			tt.SetDark(dark)
			tt.Frame()
			var width, y float32
			for i, title := range []string{"HTTP", "TLS", "QUIC", "BitTorrent"} {
				button, ok := tt.Find(title)
				if !ok {
					t.Fatal("missing protocol toggle", title)
				}
				if i == 0 {
					width, y = button.W, button.Y
				}
				if abs32(button.W-width) > 1 || button.Y != y || button.X+button.W > 320 {
					t.Fatalf("protocol buttons do not fill one row evenly: %+v", button)
				}
				if err := tt.Click(title); err != nil {
					t.Fatal(err)
				}
				tt.Frame()
			}
			if len(ruleEntries("protocol", a.rulePage.values["protocol"])) != 4 {
				t.Fatal("selecting a protocol cleared another selection")
			}
			if err := tt.Click("TLS"); err != nil {
				t.Fatal(err)
			}
			tt.Frame()
			if a.rulePage.values["protocol"] != "http\nquic\nbittorrent" {
				t.Fatal("toggle did not deselect only TLS")
			}
			raw, err := a.rulePage.matchJSON()
			if err != nil || decodeRule(&pb.RoutingRule{RuleJson: raw}).values["protocol"] != a.rulePage.values["protocol"] {
				t.Fatalf("protocol choices did not survive serialization: %s, %v", raw, err)
			}
			if err := tt.Click("Reset to Any"); err != nil {
				t.Fatal(err)
			}
			tt.Frame()
			if a.rulePage.values["protocol"] != "" {
				t.Fatal("reset did not clear all protocols")
			}
		})
	}
}
func TestRuleEditorSave(t *testing.T) {
	for _, edit := range []bool{false, true} {
		t.Run(map[bool]string{true: "edit", false: "create"}[edit], func(t *testing.T) {
			f := &ruleEditorDaemon{fakeDaemon: ukServers()}
			a := newTestApp(f.fakeDaemon)
			a.d = f
			old := &pb.RoutingRule{RuleTag: "old", RuleJson: `{"domain":["domain:example.com"]}`, Target: "direct", Disabled: true}
			if edit {
				a.rules.rules = append(a.rules.rules, old)
				a.openRule(old)
				a.editRule(old)
			} else {
				a.newRule()
				a.rulePage.values["domain"] = "domain:example.com"
			}
			a.rulePage.name = "new"
			a.saveRule()
			if a.rulePage.busy || a.rulePage.err != "" {
				t.Fatalf("save failed: %+v", a.rulePage)
			}
			r := a.ruleByTag("new")
			if r == nil {
				t.Fatal("saved rule missing")
			}
			if edit {
				if f.alter == nil || f.alter.RuleTag != "old" || f.alter.GetNewRuleTag() != "new" || !r.Disabled {
					t.Fatal("edit did not preserve identity/disabled state")
				}
				if a.path != ruleDetailsPath {
					t.Fatal(a.path)
				}
			} else if f.add == nil || !f.add.Prepend || a.path != mainPath {
				t.Fatal("create failed")
			}
		})
	}
}
func TestRuleEditorFailureRetainsDraft(t *testing.T) {
	f := &ruleEditorDaemon{fakeDaemon: ukServers(), err: errors.New("save failed")}
	a := newTestApp(f.fakeDaemon)
	a.d = f
	a.newRule()
	a.rulePage.name = "work"
	a.rulePage.values["port"] = "443"
	a.saveRule()
	if a.path != ruleEditPath || a.rulePage.busy || !strings.Contains(a.rulePage.err, "save failed") || a.ruleByTag("work") != nil {
		t.Fatalf("failed save lost draft: %+v", a.rulePage)
	}
}

func TestRuleInvalidDataCannotBeOverwritten(t *testing.T) {
	st := decodeRule(&pb.RoutingRule{RuleJson: `null`})
	st.values["port"] = "443"
	if _, err := st.matchJSON(); err == nil {
		t.Fatal("allowed overwriting invalid original data")
	}
	st = decodeRule(&pb.RoutingRule{RuleJson: `{"protocol":["future-protocol"],"network":["tcp","udp"]}`})
	st.values["port"] = "443"
	raw, err := st.matchJSON()
	if err != nil || !strings.Contains(raw, `"network":["tcp","udp"]`) {
		t.Fatalf("existing match changed: %s %v", raw, err)
	}
}

func TestUnspecifiedRuleConditionsMeanAny(t *testing.T) {
	for _, d := range ruleConditions {
		if got := conditionSummary(d.key, ""); got != "Any" {
			t.Errorf("%s: %s", d.key, got)
		}
	}
	if got := conditionSummary("port", "443"); got != "443" {
		t.Fatal(got)
	}
	if got := conditionSummary("process", "/Applications/Safari.app/"); got != "Safari" {
		t.Fatal(got)
	}
}

func TestRuleAccordionsEditInlineAndKeepOneOpen(t *testing.T) {
	a := newTestApp(ukServers())
	a.newRule()
	tt := tester(a)
	tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	tt.SetSize(panelWidth, 620)
	tt.Frame()
	tt.Frame()
	click := func(title string) {
		t.Helper()
		if err := tt.Click(title); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
		tt.Frame()
		if a.path != ruleEditPath {
			t.Fatalf("%s navigated away", title)
		}
	}
	click("Websites and domains")
	if _, ok := tt.Find("Websites and domains options"); !ok {
		t.Fatal("missing inline domain editor")
	}
	click("Domain")
	if err := tt.Click("Domain or pattern"); err != nil {
		t.Fatal(err)
	}
	tt.Type("example.com")
	tt.Frame()
	click("Add")
	if a.rulePage.values["domain"] != "domain:example.com" {
		t.Fatal("domain not applied")
	}
	click("Applications")
	if _, ok := tt.Find("Websites and domains options"); ok {
		t.Fatal("previous accordion stayed open")
	}
	if _, ok := tt.Find("Applications options"); !ok {
		t.Fatal("app editor did not open")
	}
	click("Applications")
	if a.rulePage.expanded != "" {
		t.Fatal("click did not collapse accordion")
	}
	click("Choose destination")
	if _, ok := tt.Find("Destination options"); !ok {
		t.Fatal("destination editor did not open")
	}
	click("Block")
	if a.rulePage.target != "block" || a.rulePage.expanded != "destination" {
		t.Fatal("destination not applied inline")
	}
	// Opening a condition also closes the destination.
	tt.Scroll(150, 200, 0, -1000)
	tt.Frame()
	click("Websites and domains")
	if _, ok := tt.Find("Destination options"); ok {
		t.Fatal("destination stayed expanded")
	}
	if a.rulePage.values["domain"] != "domain:example.com" {
		t.Fatal("switching lost a condition")
	}
}

func TestRuleThenFollowsExplanationAndFooterFits(t *testing.T) {
	a := newTestApp(ukServers())
	a.newRule()
	tt := tester(a)
	tt.Frame()
	tt.Frame()
	tt.SetSize(panelWidth, int(a.restHeight()))
	tt.Frame()
	tt.Frame()
	explanation, ok := tt.Find("Rule matching explanation")
	if !ok {
		t.Fatal("missing explanation")
	}
	destination, ok := tt.Find("Choose destination")
	if !ok {
		t.Fatal("missing destination")
	}
	if gap := destination.Y - explanation.Y - explanation.H; gap < 7 || gap > 24 {
		t.Fatalf("gap before Then = %v, want a normal inset", gap)
	}
	conditions, ok := tt.Find("Rule conditions")
	if !ok {
		t.Fatal("missing conditions accordion")
	}
	more, ok := tt.Find("+ more conditions")
	if !ok || more.Y < conditions.Y+conditions.H {
		t.Fatal("more matching conditions is inside the accordion")
	}
	target, ok := tt.Find("Rule destination")
	if !ok || target.Y < conditions.Y+conditions.H {
		t.Fatal("destination is not a separate accordion")
	}
	save, ok := tt.Find("Save rule")
	if !ok || save.Y+save.H > a.restHeight() {
		t.Fatalf("save footer clipped: %+v height %v", save, a.restHeight())
	}
	if a.restHeight() >= pageOf(ruleEditPath).height {
		t.Fatal("collapsed form did not size to its content")
	}
}

func TestRuleAppsApplyImmediatelyInsideAccordion(t *testing.T) {
	a := newTestApp(ukServers())
	a.newRule()
	a.ruleApps = ruleAppState{all: []ruleApp{{Name: "Example", Path: "/Example.app/", Icon: "/Example.app"}}, selected: map[string]bool{}}
	a.filterRuleApps()
	a.rulePage.expanded = "process"
	tt := tester(a)
	tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	tt.SetSize(panelWidth, 620)
	tt.Frame()
	tt.Frame()
	if err := tt.Click("Select Example"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Frame()
	if a.rulePage.values["process"] != "/Example.app/" || a.path != ruleEditPath {
		t.Fatal("app selection not applied inline")
	}
	if err := tt.Click("Select Example"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.rulePage.values["process"] != "" {
		t.Fatal("app deselection not applied")
	}
}

func TestRuleAccordionCommitsPendingEntryAndPreservesInvalidInput(t *testing.T) {
	a := newTestApp(ukServers())
	a.newRule()
	a.rulePage.expanded = "ip"
	a.rulePage.entry = "1.1.1.1"
	a.toggleRuleAccordion("destination")
	if a.rulePage.values["ip"] != "1.1.1.1" || a.rulePage.expanded != "destination" {
		t.Fatal("pending address not committed")
	}
	a.rulePage.expanded = "ip"
	a.rulePage.entry = "invalid"
	a.toggleRuleAccordion("domain")
	if a.rulePage.expanded != "ip" || a.rulePage.entry != "invalid" || a.rulePage.err == "" {
		t.Fatal("invalid entry silently lost")
	}
}

func TestRuleNativeAccordionAnimatesOpenAndClosed(t *testing.T) {
	a := newTestApp(ukServers())
	a.newRule()
	tt := tester(a)
	tt.SetSize(panelWidth, 620)
	tt.Frame()
	tt.Frame()
	nextY := func() float32 {
		t.Helper()
		r, ok := tt.Find("IP addresses and ranges")
		if !ok {
			t.Fatal("missing next header")
		}
		return r.Y
	}
	closed := nextY()
	if err := tt.Click("Websites and domains"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Advance(80 * time.Millisecond)
	mid := nextY()
	tt.Advance(300 * time.Millisecond)
	opened := nextY()
	if !(closed < mid && mid < opened) {
		t.Fatalf("no animated expansion: closed=%v mid=%v open=%v", closed, mid, opened)
	}
	if err := tt.Click("Websites and domains"); err != nil {
		t.Fatal(err)
	}
	tt.Advance(80 * time.Millisecond)
	closing := nextY()
	tt.Advance(300 * time.Millisecond)
	if !(closed < closing && closing < opened) || abs32(nextY()-closed) > 1 {
		t.Fatalf("no animated collapse: closed=%v mid=%v final=%v", closed, closing, nextY())
	}
}

func TestRuleNativeControlsHaveDistinctSurfaceDepths(t *testing.T) {
	for _, dark := range []bool{true, false} {
		t.Run(map[bool]string{true: "dark", false: "light"}[dark], func(t *testing.T) {
			a := newTestApp(ukServers())
			a.newRule()
			a.rulePage.expanded = "domain"
			a.rulePage.domainMode = 1
			tt := tester(a)
			tt.SetDark(dark)
			tt.SetPreferences(ui.Preferences{ReduceMotion: true})
			tt.SetSize(panelWidth, 620)
			tt.Frame()
			tt.Frame()
			panel, ok := tt.Find("Websites and domains options")
			if !ok {
				t.Fatal("missing native accordion panel")
			}
			input, ok := tt.Find("Domain or pattern")
			if !ok {
				t.Fatal("missing input")
			}
			card, ok := tt.Find("Rule conditions")
			if !ok {
				t.Fatal("missing accordion")
			}
			sample := func(x, y float32) [3]uint32 {
				r, g, b, _ := tt.Image().At(int(x), int(y)).RGBA()
				return [3]uint32{r, g, b}
			}
			cardColor := sample(card.X+3, card.Y+20)
			panelColor := sample(panel.X+3, panel.Y+panel.H/2)
			inputColor := sample(input.X+input.W-3, input.Y+2)
			if cardColor == panelColor || panelColor == inputColor {
				t.Fatalf("surfaces merged: card=%v panel=%v field=%v", cardColor, panelColor, inputColor)
			}
			if dark && !(inputColor[0] < panelColor[0] && panelColor[0] < cardColor[0]) {
				t.Fatalf("incorrect dark depth order: card=%v panel=%v field=%v", cardColor, panelColor, inputColor)
			}
			regex, ok := tt.Find("Domain")
			if !ok || regex.X+regex.W > panel.X+panel.W {
				t.Fatalf("native segmented control overflows: segment=%+v panel=%+v", regex, panel)
			}
		})
	}
}

func TestRuleNativeAccordionKeyboardNavigation(t *testing.T) {
	a := newTestApp(ukServers())
	a.newRule()
	tt := tester(a)
	tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	tt.SetSize(panelWidth, 620)
	tt.Frame()
	tt.Frame()
	if err := tt.Click("Applications"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.Focused("Search applications…") {
		t.Fatal("app search did not autofocus")
	}
	// Close the input-bearing panel before navigating between headers.
	if err := tt.Click("Applications"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.KeyDown(ui.KeyDown)
	tt.Frame()
	if !tt.Focused("Websites and domains") {

		t.Fatal("native accordion arrow navigation did not move focus")
	}
	tt.KeyDown(ui.KeyEnter)
	tt.Frame()
	tt.Frame()
	if a.rulePage.expanded != "domain" || a.path != ruleEditPath {
		t.Fatal("native keyboard disclosure failed")
	}
	if _, ok := tt.Find("Applications options"); ok {
		t.Fatal("previous panel remained open")
	}
}

func TestRuleAccordionHeadersStayOnOneLineWithSelections(t *testing.T) {
	a := newTestApp(ukServers())
	a.newRule()
	a.rulePage.values["domain"] = "domain:averylongwebsitename.example\nfull:second.example"
	tt := tester(a)
	tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	tt.Frame()
	tt.Frame()
	for _, title := range []string{"Applications", "Websites and domains", "IP addresses and ranges", "Target ports", "Network type", "Choose destination"} {
		r, ok := tt.Find(title)
		if !ok || r.H != 40 {
			t.Fatalf("%s header not compact: %+v", title, r)
		}
	}
	if _, ok := tt.Find("Target"); !ok {
		t.Fatal("target label missing")
	}
	if _, ok := tt.Find("+1"); !ok {
		t.Fatal("extra selection count missing")
	}
}

func TestRulePortEditorHasNoSuggestedPorts(t *testing.T) {
	a := newTestApp(ukServers())
	a.newRule()
	a.rulePage.expanded = "port"
	tt := tester(a)
	tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	tt.Frame()
	tt.Frame()
	if _, ok := tt.Find("Ports or ranges"); !ok {
		t.Fatal("missing port input")
	}
	for _, label := range []string{"HTTP 80", "HTTPS 443", "DNS 53"} {
		if tt.HasText(label) {
			t.Fatalf("unexpected suggested port: %s", label)
		}
	}
}

func TestSelectedApplicationSummaryStacksIcons(t *testing.T) {
	a := newTestApp(ukServers())
	a.newRule()
	dir := t.TempDir()
	var matches []string
	for _, name := range []string{"One.app", "Two.app", "Three.app"} {
		// The summary only loads icons for native absolute paths, including a
		// drive letter on Windows. Unix-rooted fixtures skip that branch there.
		path := filepath.Join(dir, name)
		a.apps.known[path] = &appMeta{name: path, icon: ui.NewBitmap(image.NewRGBA(image.Rect(0, 0, 32, 32)))}
		matches = append(matches, path+"/")
	}
	a.rulePage.values["process"] = strings.Join(append(matches, "my-worker"), "\n")
	tt := tester(a)
	tt.Frame()
	tt.Frame()
	stack, ok := tt.Find("Selected application icons")
	if !ok || stack.W != 52 || stack.H != 24 {
		t.Fatalf("missing icon stack: %+v", stack)
	}
	if _, ok := tt.Find("+1"); !ok {
		t.Fatal("missing extra selection count")
	}
	header, _ := tt.Find("Applications")
	if stack.Y < header.Y || stack.Y+stack.H > header.Y+header.H {
		t.Fatal("icon stack escaped header")
	}
}

func TestDomainMatchersPreservePrefixesAndRegexCommas(t *testing.T) {
	a := newTestApp(ukServers())
	a.rulePage = decodeRule(nil)
	a.rulePage.entry = `regexp:^[a-z]{1,3}\.example\.com$`
	a.addRuleConditionEntry("domain")
	want := a.rulePage.values["domain"]
	if want != `regexp:^[a-z]{1,3}\.example\.com$` {
		t.Fatalf("changed regex: %q", want)
	}
	a.rulePage.entry = "example.com, full:www.example.org, keyword:news"
	a.addRuleConditionEntry("domain")
	if got := a.rulePage.values["domain"]; got != want+"\ndomain:example.com\nfull:www.example.org\nkeyword:news" {
		t.Fatalf("changed matcher semantics: %q", got)
	}
	if _, err := a.rulePage.matchJSON(); err != nil {
		t.Fatal(err)
	}
}

func TestRuleConditionInputsAutofocusOnEveryOpen(t *testing.T) {
	for _, tc := range []struct {
		key, input string
		mode       int
	}{
		{"process", "Search applications…", 0},
		{"process", "Application paths", 1},
		{"domain", "Search category or website…", 0},
		{"domain", "Domain or pattern", 1},
		{"ip", "Search country or GeoIP category…", 0},
		{"ip", "IP addresses or ranges", 1},
		{"source", "IP addresses or ranges", 0},
		{"port", "Ports or ranges", 0},
		{"sourcePort", "Ports or ranges", 0},
		{"destination", "Search servers…", 0},
	} {
		t.Run(tc.key+tc.input, func(t *testing.T) {
			a := newTestApp(ukServers())
			a.newRule()
			a.rulePage.more = true
			a.rulePage.domainMode, a.rulePage.ipMode = tc.mode, tc.mode
			a.ruleApps.tab = tc.mode
			tt := tester(a)
			tt.SetSize(panelWidth, 620)
			tt.Frame()
			if tt.Focused("Rule name") {
				t.Fatal("rule name took initial focus")
			}
			for i := 0; i < 2; i++ {
				a.toggleRuleAccordion(tc.key)
				tt.Frame()
				tt.Frame()
				if !tt.Focused(tc.input) {
					t.Fatalf("input not focused on open %d", i)
				}
				if err := tt.Click("Rule name"); err != nil {
					t.Fatal(err)
				}
				tt.Frame()
				if !tt.Focused("Rule name") {
					t.Fatal("input repeatedly stole focus")
				}
				a.toggleRuleAccordion(tc.key)
				tt.Frame()
				tt.Frame()
			}
		})
	}
}

func TestRuleNameFollowsConditionsAndTarget(t *testing.T) {
	a := newTestApp(ukServers())
	a.newRule()
	tt := tester(a)
	tt.SetSize(panelWidth, 620)
	tt.Frame()
	tt.Frame()
	name, _ := tt.Find("Rule name input")
	target, _ := tt.Find("Rule destination")
	save, _ := tt.Find("Save rule")
	more, ok := tt.Find("+ more conditions")
	card, _ := tt.Find("Rule conditions")
	if name.Y < target.Y+target.H || name.Y+name.H > save.Y {
		t.Fatalf("name is not between target and actions: %v %v %v", target, name, save)
	}
	if !ok || abs32(more.X-card.X) > 1 || more.W >= card.W {
		t.Fatal("more conditions is not a left-aligned text control")
	}
	if err := tt.Click("+ more conditions"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if _, ok := tt.Find("- less conditions"); !ok {
		t.Fatal("missing less conditions control")
	}
}

func TestRuleCreateAfterRefreshShowsOneRow(t *testing.T) {
	f := &ruleEditorDaemon{fakeDaemon: ukServers()}
	a := newTestApp(f.fakeDaemon)
	a.d = f
	a.newRule()
	a.rulePage.name = "new"
	a.rulePage.values["domain"] = "domain:example.com"
	var pending []func()
	a.post = func(fn func()) { pending = append(pending, fn) }
	a.saveRule()
	// The daemon's revision refresh reaches the UI before AddRouting's completion.
	f.rules = append([]*pb.RoutingRule{{RuleTag: "new", RuleJson: `{"domain":["domain:example.com"]}`, Target: "proxy"}}, f.rules...)
	a.refreshLists()
	if len(pending) != 2 {
		t.Fatalf("expected save and refresh completions, got %d", len(pending))
	}
	pending[1]()
	pending[0]()
	rows := 0
	for _, item := range a.ruleListItems(a.rules.rules) {
		if item.rule != nil && item.rule.RuleTag == "new" {
			rows++
		}
	}
	if rows != 1 {
		t.Fatalf("new rule appears in %d UI rows; storage contains one", rows)
	}
}
