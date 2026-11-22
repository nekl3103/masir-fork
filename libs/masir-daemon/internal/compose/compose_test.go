package compose

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Gozargah/Masir/libs/masir-daemon/internal/store"
	"github.com/Gozargah/Masir/libs/masir-daemon/internal/subs"
	"github.com/Gozargah/Masir/libs/masir-daemon/internal/xray"
)

const (
	activeOut = `{"protocol":"trojan","settings":{"servers":[{"address":"active.example.com","port":443,"password":"x"}]},"streamSettings":{"security":"tls"}}`
	pinnedOut = `{"protocol":"trojan","settings":{"servers":[{"address":"pinned.example.com","port":443,"password":"y"}]},"streamSettings":{"security":"tls"}}`
)

type config struct {
	Log map[string]any `json:"log"`
	DNS struct {
		Servers []map[string]any `json:"servers"`
	} `json:"dns"`
	Inbounds  []map[string]any `json:"inbounds"`
	Outbounds []map[string]any `json:"outbounds"`
	Routing   struct {
		DomainStrategy string           `json:"domainStrategy"`
		Balancers      []map[string]any `json:"balancers"`
		Rules          []map[string]any `json:"rules"`
	} `json:"routing"`
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func testState(t *testing.T) store.State {
	t.Helper()
	st := store.State{Settings: store.DefaultSettings()}
	st.Settings.AllowLAN = false
	st.Settings.ProxyPort = freePort(t)
	return st
}

func build(t *testing.T, in Input) (Output, config) {
	t.Helper()
	out, err := Build(in)
	if err != nil {
		t.Fatal(err)
	}
	var c config
	if err := json.Unmarshal(out.Config, &c); err != nil {
		t.Fatal(err)
	}
	return out, c
}

func tags(items []map[string]any, key string) []string {
	var out []string
	for _, it := range items {
		s, _ := it[key].(string)
		out = append(out, s)
	}
	return out
}

func ruleByTag(c config, tag string) map[string]any {
	for _, r := range c.Routing.Rules {
		if r["ruleTag"] == tag {
			return r
		}
	}
	return nil
}

// fallbackSelector is where the catch-all's balancer sends traffic.
func fallbackSelector(c config) []any {
	for _, b := range c.Routing.Balancers {
		if b["tag"] == FallbackBalancer {
			s, _ := b["selector"].([]any)
			return s
		}
	}
	return nil
}

func TestBuildFollowsMasirTS(t *testing.T) {
	st := testState(t)
	st.Settings.Fallback = store.FallbackProxy
	if err := st.AddRule("ir-direct", []byte(`{"ip":["1.2.3.0/24"]}`), store.Target{Kind: store.TargetDirect}, false); err != nil {
		t.Fatal(err)
	}
	if err := st.AddRule("pin", []byte(`{"domain":["full:pinned.test"]}`),
		store.Target{Kind: store.TargetConfig, SubscriptionID: "s1", ConfigID: "c1", Name: "Pinned"}, false); err != nil {
		t.Fatal(err)
	}
	resolve := func(tg store.Target) (subs.Config, bool) {
		return subs.Config{ID: "c1", Name: "Pinned", Outbound: json.RawMessage(pinnedOut)}, tg.ConfigID == "c1"
	}

	out, c := build(t, Input{State: st, Proxy: true, Active: json.RawMessage(activeOut), Resolve: resolve})

	if got := tags(c.Outbounds, "tag"); !slices.Equal(got, []string{"direct", "block", "proxy", "sub-s1-c1", "dns-out"}) {
		t.Errorf("outbounds = %v", got)
	}
	want := []string{"masir-anti-loop", "masir-dns-bootstrap", "masir-dns-hijack", "masir-dns-upstream",
		"ir-direct", "pin", "masir-lan-bypass", "masir-proxy-default-route", "masir-default-route"}
	if got := tags(c.Routing.Rules, "ruleTag"); !slices.Equal(got, want) {
		t.Errorf("rules = %v\nwant    %v", got, want)
	}
	if sel := fallbackSelector(c); !slices.Equal(sel, []any{"proxy"}) {
		t.Errorf("fallback balancer = %v", c.Routing.Balancers)
	}
	if r := ruleByTag(c, "masir-default-route"); r["balancerTag"] != FallbackBalancer {
		t.Errorf("fallback rule = %v", r)
	}
	if r := ruleByTag(c, "pin"); r["outboundTag"] != "sub-s1-c1" {
		t.Errorf("pinned rule = %v", r)
	}
	if r := ruleByTag(c, "masir-dns-hijack"); r["network"] != "tcp,udp" {
		t.Errorf("dns hijack rule network = %v, want tcp,udp", r["network"])
	}
	if c.Routing.DomainStrategy != "IPIfNonMatch" {
		t.Errorf("an ip rule should switch domainStrategy, got %q", c.Routing.DomainStrategy)
	}

	// DoH first; bootstrap servers resolve the proxy servers' names, direct.
	if c.DNS.Servers[0]["address"] != st.Settings.DoHDNS {
		t.Errorf("first dns server = %v", c.DNS.Servers[0])
	}
	boot := c.DNS.Servers[1]
	if boot["tag"] != "dns-bootstrap" || boot["address"] != "1.1.1.1" {
		t.Errorf("bootstrap = %v", boot)
	}
	domains, _ := json.Marshal(boot["domains"])
	for _, d := range []string{"full:active.example.com", "full:pinned.example.com"} {
		if !strings.Contains(string(domains), d) {
			t.Errorf("bootstrap domains %s miss %s", domains, d)
		}
	}

	mixed := c.Inbounds[0]
	if mixed["tag"] != ProxyInboundTag || mixed["protocol"] != "mixed" || mixed["listen"] != "127.0.0.1" || mixed["sniffing"] == nil {
		t.Errorf("proxy inbound = %v", mixed)
	}
	if len(out.Rules) != 2 || out.Rules[1].FellBack {
		t.Errorf("resolved = %+v", out.Rules)
	}

	// It has to actually run.
	p, err := xray.ParseProfile(out.Config)
	if err != nil {
		t.Fatal(err)
	}
	core := xray.New("")
	defer core.Close()
	if err := core.Apply(context.Background(), p); err != nil {
		t.Fatalf("xray rejected the composed config: %v", err)
	}
	for domain, want := range map[string]string{"pinned.test": "sub-s1-c1", "anything.test": "proxy"} {
		res, err := core.TestRoute(context.Background(), xray.RouteQuery{Domain: domain, Port: 443})
		if err != nil || res.OutboundTag != want {
			t.Errorf("route %s = %+v, %v; want %s", domain, res, err, want)
		}
	}
	if res, _ := core.TestRoute(context.Background(), xray.RouteQuery{IP: "192.168.1.10", Port: 80}); res.OutboundTag != "direct" {
		t.Errorf("LAN address routed to %s, want direct", res.OutboundTag)
	}
}

func TestBuildWithoutSelection(t *testing.T) {
	st := testState(t)
	st.Settings.Fallback = store.FallbackProxy // e.g. the selection's subscription is long gone
	_ = st.AddRule("p", []byte(`{"domain":["full:p.test"]}`), store.Target{Kind: store.TargetProxy}, false)
	_ = st.AddRule("gone", []byte(`{"domain":["full:g.test"]}`),
		store.Target{Kind: store.TargetConfig, SubscriptionID: "s", ConfigID: "missing"}, false)

	out, c := build(t, Input{State: st, Proxy: true, Resolve: func(store.Target) (subs.Config, bool) { return subs.Config{}, false }})

	for _, tag := range []string{"masir-dns-upstream", "p", "gone"} {
		if r := ruleByTag(c, tag); r["outboundTag"] != "direct" {
			t.Errorf("%s = %v; with no proxy it must go direct", tag, r["outboundTag"])
		}
	}
	if got := fallbackSelector(c); !slices.Equal(got, []any{"direct"}) {
		t.Errorf("fallback = %v; with no proxy it must go direct", got)
	}
	if slices.Contains(tags(c.Outbounds, "tag"), "proxy") {
		t.Error("no selection, yet a proxy outbound")
	}
	if r := ruleByTag(c, "masir-proxy-default-route"); r["outboundTag"] != "block" {
		t.Errorf("mixed proxy fallback without selection = %v, want block", r)
	}
	for _, rr := range out.Rules {
		if !rr.FellBack {
			t.Errorf("rule %s should report falling back", rr.Rule.Tag)
		}
	}
}

func TestMixedProxyFallbackWithDirectMode(t *testing.T) {
	st := testState(t)
	st.Settings.Fallback = store.FallbackDirect
	if err := st.AddRule("allow-direct", []byte(`{"domain":["full:direct.test"]}`), store.Target{Kind: store.TargetDirect}, false); err != nil {
		t.Fatal(err)
	}
	if err := st.AddRule("deny", []byte(`{"domain":["full:blocked.test"]}`), store.Target{Kind: store.TargetBlock}, false); err != nil {
		t.Fatal(err)
	}
	out, _ := build(t, Input{State: st, Proxy: true, Active: json.RawMessage(activeOut)})
	p, err := xray.ParseProfile(out.Config)
	if err != nil {
		t.Fatal(err)
	}
	core := xray.New("")
	defer core.Close()
	ctx := context.Background()
	if err := core.Apply(ctx, p); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		query xray.RouteQuery
		want  string
	}{
		{xray.RouteQuery{InboundTag: ProxyInboundTag, Domain: "other.test", Port: 443}, "proxy"},
		{xray.RouteQuery{InboundTag: ProxyInboundTag, Domain: "other.test", Port: 443, Network: "udp"}, "proxy"},
		{xray.RouteQuery{InboundTag: ProxyInboundTag, Domain: "direct.test", Port: 443}, "direct"},
		{xray.RouteQuery{InboundTag: ProxyInboundTag, Domain: "blocked.test", Port: 443}, "block"},
		{xray.RouteQuery{InboundTag: TunInboundTag, Domain: "other.test", Port: 443}, "direct"},
	} {
		got, err := core.TestRoute(ctx, tt.query)
		if err != nil || !got.Matched || got.OutboundTag != tt.want {
			t.Errorf("route %+v = %+v, %v; want %s", tt.query, got, err, tt.want)
		}
	}
}

func TestBuildPinnedFallsBackToProxy(t *testing.T) {
	st := testState(t)
	st.Settings.Fallback = store.FallbackDirect
	_ = st.AddRule("gone", []byte(`{"domain":["full:g.test"]}`),
		store.Target{Kind: store.TargetConfig, SubscriptionID: "s", ConfigID: "missing"}, false)
	out, c := build(t, Input{State: st, Proxy: true, Active: json.RawMessage(activeOut),
		Resolve: func(store.Target) (subs.Config, bool) { return subs.Config{}, false }})
	if r := ruleByTag(c, "gone"); r["outboundTag"] != "proxy" || !out.Rules[0].FellBack {
		t.Fatalf("rule = %v, resolved = %+v", r, out.Rules)
	}
	if sel := fallbackSelector(c); !slices.Equal(sel, []any{"direct"}) {
		t.Fatalf("fallback direct should stay direct, got %v", c.Routing.Balancers)
	}
}

// Switching the routing mode must not rewrite rules: the catch-all stays put and only
// the balancer it points at is retargeted, so nothing Xray is carrying is interrupted.
func TestFallbackOnlyRetargetsTheBalancer(t *testing.T) {
	st := testState(t)
	active := json.RawMessage(activeOut)
	st.Settings.Fallback = store.FallbackDirect
	_, direct := build(t, Input{State: st, Proxy: true, Active: active})
	st.Settings.Fallback = store.FallbackProxy
	_, proxy := build(t, Input{State: st, Proxy: true, Active: active})

	if !reflect.DeepEqual(direct.Routing.Rules, proxy.Routing.Rules) {
		t.Errorf("rules changed with the mode:\n%v\n%v", direct.Routing.Rules, proxy.Routing.Rules)
	}
	if !reflect.DeepEqual(direct.Inbounds, proxy.Inbounds) || !reflect.DeepEqual(direct.Outbounds, proxy.Outbounds) {
		t.Error("only the fallback may change with the mode")
	}
	if got := fallbackSelector(direct); !slices.Equal(got, []any{"direct"}) {
		t.Errorf("direct balancer = %v", got)
	}
	if got := fallbackSelector(proxy); !slices.Equal(got, []any{"proxy"}) {
		t.Errorf("proxy balancer = %v", got)
	}
}

func TestBuildInbounds(t *testing.T) {
	st := testState(t)
	st.Settings.AllowLAN = true

	_, c := build(t, Input{State: st, Proxy: true, Tun: true, TunName: "utun42"})
	if c.Inbounds[0]["listen"] != "0.0.0.0" {
		t.Errorf("allow LAN should listen on all interfaces: %v", c.Inbounds[0])
	}
	if _, ok := c.Inbounds[0]["sniffing"]; ok {
		t.Error("no user rules, no sniffing on the proxy")
	}
	tun := c.Inbounds[1]
	settings, _ := tun["settings"].(map[string]any)
	if tun["tag"] != TunInboundTag || tun["protocol"] != "tun" || settings["name"] != "utun42" ||
		settings["autoOutboundsInterface"] != "auto" {
		t.Errorf("tun inbound = %v", tun)
	}
	if dns, _ := json.Marshal(settings["dns"]); string(dns) != `["1.1.1.1","8.8.8.8"]` {
		t.Errorf("tun dns should fall back to dns servers, got %s", dns)
	}
	if gw, _ := json.Marshal(settings["gateway"]); string(gw) != `["10.10.20.1/24","fd00::1/64"]` {
		t.Errorf("tun gateway should include ipv4 and ipv6, got %s", gw)
	}

	_, off := build(t, Input{State: st})
	if len(off.Inbounds) != 0 {
		t.Errorf("proxy off should have no inbounds, got %v", off.Inbounds)
	}
}

func TestTunBlocksDNSLeaksOnWindows(t *testing.T) {
	st := testState(t)
	tunSettings := func(os string) map[string]any {
		goos = os
		defer func() { goos = runtime.GOOS }()
		_, c := build(t, Input{State: st, Proxy: true, Tun: true})
		settings, _ := c.Inbounds[1]["settings"].(map[string]any)
		return settings
	}
	win := tunSettings("windows")
	if got, _ := json.Marshal(win["autoSystemWfpBlockLeak"]); string(got) != `["dns","misconfigtun"]` {
		t.Errorf("windows: autoSystemWfpBlockLeak = %s, want [\"dns\",\"misconfigtun\"]", got)
	}
	if win["desc"] != "Masir" {
		t.Errorf("windows: desc = %v, want Masir", win["desc"])
	}
	for _, os := range []string{"linux", "freebsd"} {
		s := tunSettings(os)
		if s["autoSystemDnsToGateway"] != true {
			t.Errorf("%s: autoSystemDnsToGateway = %v, want true", os, s["autoSystemDnsToGateway"])
		}
	}
	mac := tunSettings("darwin")
	if mac["autoSystemWfpBlockLeak"] != nil {
		t.Errorf("the filter is Windows-only, got %v on macOS", mac["autoSystemWfpBlockLeak"])
	}
	if mac["autoSystemDnsToGateway"] != nil {
		t.Errorf("autoSystemDnsToGateway is Linux/FreeBSD only, got %v on macOS", mac["autoSystemDnsToGateway"])
	}
}

func TestXrayLogSettings(t *testing.T) {
	dir := t.TempDir()
	st := testState(t)
	st.Settings.XrayLogLevel = "info"
	_, c := build(t, Input{State: st, LogDir: dir})
	if c.Log["loglevel"] != "info" {
		t.Fatalf("loglevel = %v, want info", c.Log["loglevel"])
	}
	if got, _ := c.Log["error"].(string); got != filepath.Join(dir, "xray-error.log") {
		t.Fatalf("error log = %q", got)
	}

	// Without a log directory the config stays on stdout, which is what tests and
	// callers that only inspect the config want.
	_, c = build(t, Input{State: st})
	if _, ok := c.Log["error"]; ok {
		t.Fatalf("error log should be unset without LogDir: %v", c.Log)
	}
}

// A root daemon refuses configs that point Xray at local files (xray.CheckNoLocalFiles).
// What masir composes itself must never trip that, on any OS, or no privileged user could
// connect.
func TestComposedConfigNeverAsksForLocalFiles(t *testing.T) {
	st := testState(t)
	st.Settings.AllowLAN = true
	st.Settings.Fallback = store.FallbackProxy
	if err := st.AddRule("a", []byte(`{"process":["Telegram","/Applications/Spotify.app/"],"domain":["full:google.com"]}`), store.Target{Kind: store.TargetProxy}, false); err != nil {
		t.Fatal(err)
	}
	if err := st.AddRule("pin", []byte(`{"domain":["full:pinned.test"]}`),
		store.Target{Kind: store.TargetConfig, SubscriptionID: "s1", ConfigID: "c1", Name: "Pinned"}, false); err != nil {
		t.Fatal(err)
	}
	resolve := func(tg store.Target) (subs.Config, bool) {
		return subs.Config{ID: "c1", Name: "Pinned", Outbound: json.RawMessage(pinnedOut)}, tg.ConfigID == "c1"
	}
	for _, os := range []string{"darwin", "linux", "windows"} {
		goos = os
		out, err := Build(Input{State: st, Proxy: true, Tun: true, TunName: "utun9", LogDir: "/var/run/masir/501",
			Active: json.RawMessage(activeOut), Resolve: resolve})
		goos = runtime.GOOS
		if err != nil {
			t.Fatal(err)
		}
		if err := xray.CheckNoLocalFiles(out.Config); err != nil {
			t.Errorf("%s: masir's own config was refused: %v", os, err)
		}
	}
}

func TestDisabledAndSuspendedRulesAreLeftOut(t *testing.T) {
	st := testState(t)
	for _, tag := range []string{"keep", "off", "suspended"} {
		if err := st.AddRule(tag, []byte(`{"domain":["full:`+tag+`.test"]}`), store.Target{Kind: store.TargetDirect}, false); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.SetRuleEnabled("off", false, "no longer valid"); err != nil {
		t.Fatal(err)
	}
	out, c := build(t, Input{State: st, Proxy: true, Skip: map[string]bool{"suspended": true}})
	got := tags(c.Routing.Rules, "ruleTag")
	if !slices.Contains(got, "keep") || slices.Contains(got, "off") || slices.Contains(got, "suspended") {
		t.Errorf("rules = %v", got)
	}
	if len(out.Rules) != 1 || out.Rules[0].Rule.Tag != "keep" {
		t.Errorf("resolved = %+v", out.Rules)
	}
}

func TestRulesSwitchedOffLeavesAllUserRulesOut(t *testing.T) {
	st := testState(t)
	if err := st.AddRule("direct-site", []byte(`{"domain":["full:a.test"]}`), store.Target{Kind: store.TargetDirect}, false); err != nil {
		t.Fatal(err)
	}
	st.Settings.RulesEnabled = false
	out, c := build(t, Input{State: st, Proxy: true})
	if slices.Contains(tags(c.Routing.Rules, "ruleTag"), "direct-site") || len(out.Rules) != 0 {
		t.Errorf("rules = %v, resolved = %+v", tags(c.Routing.Rules, "ruleTag"), out.Rules)
	}
	if len(st.Rules) != 1 || st.Rules[0].Disabled {
		t.Errorf("the saved rule changed: %+v", st.Rules)
	}
	if !slices.Contains(tags(c.Routing.Rules, "ruleTag"), "masir-default-route") {
		t.Error("the fallback route is gone")
	}
}

func TestBalancedProfileRoutesThroughProviderBalancer(t *testing.T) {
	cs, err := subs.Parse([]byte(`{
		"remarks":"Pool",
		"inbounds":[{"tag":"socks","protocol":"socks"},{"tag":"http","protocol":"http"}],
		"outbounds":[
			{"tag":"member-a","protocol":"freedom"},
			{"tag":"member-b","protocol":"freedom"},
			{"tag":"proxy","protocol":"trojan","settings":{"servers":[{"address":"bootstrap.example.com","port":443,"password":"fixture"}]},"streamSettings":{"security":"tls"}}
		],
		"routing":{"rules":[{"type":"field","inboundTag":["socks","http"],"balancerTag":"pool"}],
			"balancers":[{"tag":"pool","selector":["member-"],"strategy":{"type":"roundRobin"}}]}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	entry := cs[0]
	st := testState(t)
	st.Settings.Fallback = store.FallbackProxy
	st.Settings.RulesEnabled = true
	st.Rules = []store.Rule{
		{Tag: "use-selected", Match: json.RawMessage(`{"domain":["full:selected.example"]}`), Target: store.Target{Kind: store.TargetProxy}},
		{Tag: "use-pinned", Match: json.RawMessage(`{"domain":["full:pinned.example"]}`), Target: store.Target{Kind: store.TargetConfig, SubscriptionID: "feed", ConfigID: entry.ID}},
	}
	in := Input{State: st, Active: entry.Outbound, ActiveProfile: entry.Profile, Resolve: func(store.Target) (subs.Config, bool) {
		return entry, true
	}}
	// Keep the same endpoint DNS and pinned pool across the three applies.
	// A routing-only selection must leave the Xray instance running.
	plain := in
	plain.ActiveProfile = nil
	plainOut, _ := build(t, plain)
	out, c := build(t, in)
	active, _ := ruleByTag(c, "masir-default-route")["outboundTag"].(string)
	if !strings.HasPrefix(active, "masir-profile-route/masir-profile/active-") {
		t.Fatalf("unexpected default balancer %q", active)
	}
	activeMembers := strings.TrimPrefix(active, "masir-profile-route/") + "member-"
	pinned := "masir-profile-route/masir-profile/feed/" + entry.ID + "/pinned/"
	for tag, want := range map[string]string{
		"masir-default-route": active,
		"masir-dns-upstream":  active,
		"use-selected":        active,
		"use-pinned":          pinned,
	} {
		rule := ruleByTag(c, tag)
		if rule["outboundTag"] != want || rule["balancerTag"] != nil {
			t.Errorf("%s = %v, want profile route %s", tag, rule, want)
		}
	}
	engine := xray.New("")
	t.Cleanup(func() { _ = engine.Close() })
	p, err := xray.ParseProfile(out.Config)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	plainProfile, err := xray.ParseProfile(plainOut.Config)
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Apply(ctx, plainProfile); err != nil {
		t.Fatal(err)
	}
	_, generation, _, err := engine.OutboundTraffic(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Apply(ctx, p); err != nil {
		t.Fatal(err)
	}
	for domain, prefix := range map[string]string{
		"public.example":   activeMembers,
		"selected.example": activeMembers,
		"pinned.example":   "masir-profile/feed/" + entry.ID + "/pinned/member-",
	} {
		got, err := engine.TestRoute(context.Background(), xray.RouteQuery{Domain: domain, Port: 443})
		if err != nil || !strings.HasPrefix(got.OutboundTag, prefix) {
			t.Errorf("route %s = %+v, %v, want %s", domain, got, err, prefix)
		}
	}
	if err := engine.Apply(ctx, plainProfile); err != nil {
		t.Fatal(err)
	}
	got, err := engine.TestRoute(ctx, xray.RouteQuery{Domain: "public.example", Port: 443})
	if err != nil || got.OutboundTag != "proxy" {
		t.Fatalf("restored single-outbound route: %+v, %v", got, err)
	}
	_, after, _, err := engine.OutboundTraffic(ctx)
	if err != nil || generation != after {
		t.Fatalf("Xray restarted during a routing-only switch: generation %d to %d, %v", generation, after, err)
	}
}

func TestObserverStartsLiveAndUpdatesBalancerAfterFallback(t *testing.T) {
	probeReady := make(chan struct{})
	var release sync.Once
	unblock := func() { release.Do(func() { close(probeReady) }) }
	probe := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-probeReady:
			w.WriteHeader(http.StatusNoContent)
		case <-r.Context().Done():
		}
	}))
	defer probe.Close()
	defer unblock()

	// The provider lists its fallback first. Only the second member can
	// pass the health check, so the source of the decision is observable.
	entry := &subs.OutboundProfile{
		Outbounds: []json.RawMessage{
			json.RawMessage(`{"tag":"pool-z","protocol":"blackhole"}`),
			json.RawMessage(`{"tag":"pool-a","protocol":"freedom"}`),
			json.RawMessage(`{"tag":"hostname","protocol":"trojan","settings":{"servers":[{"address":"new-endpoint.example","port":443,"password":"fixture"}]},"streamSettings":{"security":"tls"}}`),
		},
		Routing: json.RawMessage(`{"rules":[{"network":"tcp,udp","balancerTag":"pool"}],
			"balancers":[{"tag":"pool","selector":["pool-"],"strategy":{"type":"leastPing"}}]}`),
		DefaultBalancer: "pool",
		Observatory:     json.RawMessage(`{"subjectSelector":["pool-"],"probeInterval":"25ms","enableConcurrency":true,"probeUrl":"` + probe.URL + `"}`),
	}
	st := testState(t)
	st.Settings.Fallback = store.FallbackProxy
	engine := xray.New("")
	defer engine.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	apply := func(in Input) {
		t.Helper()
		out, _ := build(t, in)
		profile, err := xray.ParseProfile(out.Config)
		if err != nil {
			t.Fatal(err)
		}
		if err := engine.Apply(ctx, profile); err != nil {
			t.Fatal(err)
		}
	}
	plain := Input{State: st, Active: json.RawMessage(`{"protocol":"freedom"}`)}
	apply(plain)
	_, generation, _, _ := engine.OutboundTraffic(ctx)
	apply(Input{State: st, ActiveProfile: entry})
	route := func() string {
		t.Helper()
		got, err := engine.TestRoute(ctx, xray.RouteQuery{Domain: "public.example", Port: 443})
		if err != nil {
			t.Fatal(err)
		}
		return got.OutboundTag
	}
	if got := route(); !strings.HasSuffix(got, "/pool-z") {
		t.Fatalf("initial fallback = %s", got)
	}
	unblock()
	for got := route(); !strings.HasSuffix(got, "/pool-a"); got = route() {
		select {
		case <-ctx.Done():
			t.Fatalf("observer did not move traffic to healthy member: %s", got)
		case <-time.After(20 * time.Millisecond):
		}
	}
	apply(plain)
	_, after, _, _ := engine.OutboundTraffic(ctx)
	if generation != after || route() != "proxy" {
		t.Fatalf("switch did not restore the single outbound in the same instance: generation %d to %d", generation, after)
	}
}

func TestProviderDestinationRuleStaysInsideProfile(t *testing.T) {
	profile := &subs.OutboundProfile{
		Outbounds: []json.RawMessage{
			json.RawMessage(`{"tag":"node-a","protocol":"freedom"}`),
			json.RawMessage(`{"tag":"deny","protocol":"blackhole"}`),
		},
		Routing: json.RawMessage(`{"rules":[
			{"domain":["full:denied.example"],"outboundTag":"deny"},
			{"inboundTag":["socks","http"],"balancerTag":"pool"}],
			"balancers":[{"tag":"pool","selector":["node-"]}]}`),
		Ingress:         []subs.ProfileIngress{{Tag: "socks", Protocol: "socks"}, {Tag: "http", Protocol: "http"}},
		DefaultBalancer: "pool",
	}
	st := testState(t)
	st.Settings.Fallback = store.FallbackProxy
	out, _ := build(t, Input{State: st, ActiveProfile: profile})
	parsed, err := xray.ParseProfile(out.Config)
	if err != nil {
		t.Fatal(err)
	}
	engine := xray.New("")
	defer engine.Close()
	ctx := context.Background()
	if err := engine.Apply(ctx, parsed); err != nil {
		t.Fatal(err)
	}
	for host, want := range map[string]string{"denied.example": "/deny", "ordinary.example": "/node-a"} {
		got, err := engine.TestRoute(ctx, xray.RouteQuery{Domain: host, Port: 443})
		if err != nil || !strings.HasSuffix(got.OutboundTag, want) {
			t.Fatalf("route for %s: %+v, %v", host, got, err)
		}
	}
}

func TestProfileSwitchKeepsOpenTCPAndUDPFlows(t *testing.T) {
	tcp, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tcp.Close()
	go func() {
		if conn, err := tcp.Accept(); err == nil {
			defer conn.Close()
			_, _ = io.Copy(conn, conn)
		}
	}()
	udp, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer udp.Close()
	go func() {
		var packet [64]byte
		for {
			n, from, err := udp.ReadFrom(packet[:])
			if err != nil {
				return
			}
			_, _ = udp.WriteTo(packet[:n], from)
		}
	}()

	pool := &subs.OutboundProfile{
		Outbounds: []json.RawMessage{json.RawMessage(`{"tag":"node","protocol":"freedom"}`)},
		Routing: json.RawMessage(`{"rules":[{"network":"tcp,udp","balancerTag":"pool"}],
			"balancers":[{"tag":"pool","selector":["node"]}]}`),
		DefaultBalancer: "pool",
	}
	st := testState(t)
	st.Settings.Fallback = store.FallbackProxy
	st.Settings.RulesEnabled = true
	st.Rules = []store.Rule{{Tag: "echo-through-proxy", Match: json.RawMessage(`{"ip":["127.0.0.1/32"]}`), Target: store.Target{Kind: store.TargetProxy}}}
	engine := xray.New("")
	defer engine.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	apply := func(input Input) {
		t.Helper()
		out, _ := build(t, input)
		p, err := xray.ParseProfile(out.Config)
		if err != nil {
			t.Fatal(err)
		}
		if err := engine.Apply(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	apply(Input{State: st, ActiveProfile: pool, ActiveSubscriptionID: "feed", ActiveID: "pool"})
	var connections []net.Conn
	for network, addr := range map[string]string{"tcp": tcp.Addr().String(), "udp": udp.LocalAddr().String()} {
		conn, err := engine.Dial(ctx, network, addr)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		connections = append(connections, conn)
	}
	exchange := func(message string) {
		t.Helper()
		for _, conn := range connections {
			if _, err := conn.Write([]byte(message)); err != nil {
				t.Fatal(err)
			}
			got := make([]byte, len(message))
			if _, err := io.ReadFull(conn, got); err != nil || string(got) != message {
				t.Fatalf("echo: %q, %v", got, err)
			}
		}
	}
	exchange("before switch")
	initial, generation, _, err := engine.OutboundTraffic(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var up uint64
	for tag, counters := range initial {
		if strings.HasPrefix(tag, "masir-profile/feed/pool/") {
			up += counters.Up
		}
	}
	if up < 2*uint64(len("before switch")) {
		t.Fatal("echo traffic did not pass through the balanced profile")
	}
	apply(Input{State: st, Active: json.RawMessage(`{"protocol":"freedom"}`)})
	exchange("after switch")
	traffic, next, _, err := engine.OutboundTraffic(ctx)
	if err != nil || next != generation {
		t.Fatalf("Xray restarted during the switch: %d to %d, %v", generation, next, err)
	}
	for tag, counters := range traffic {
		if strings.HasPrefix(tag, "masir-flow/masir-profile/feed/pool/") &&
			counters.Up >= 2*uint64(len("before switch")+len("after switch")) {
			return
		}
	}
	// Stock Xray has no flow-boundary counters. Patched builds must report them.
	if os.Getenv("MASIR_XRAY_SOURCE") != "" {
		t.Fatal("old profile did not retain its traffic counters across the switch")
	}
}

func TestProfileRoutesThroughInternalFailover(t *testing.T) {
	entry := &subs.OutboundProfile{
		Outbounds: []json.RawMessage{
			json.RawMessage(`{"tag":"entry","protocol":"blackhole"}`),
			json.RawMessage(`{"tag":"retry","protocol":"loopback","settings":{"inboundTag":"next"}}`),
			json.RawMessage(`{"tag":"healthy","protocol":"freedom"}`),
		},
		Observatory: json.RawMessage(`{"subjectSelector":[]}`),
		Routing: json.RawMessage(`{
			"rules":[
				{"inboundTag":["next"],"balancerTag":"second"},
				{"domain":["full:blocked.example"],"outboundTag":"entry"},
				{"port":"443","balancerTag":"first"}
			],
			"balancers":[
				{"tag":"first","selector":["entry"],"fallbackTag":"retry","strategy":{"type":"leastPing"}},
				{"tag":"second","selector":["healthy"]}
			]
		}`),
	}
	st := testState(t)
	st.Settings.Fallback = store.FallbackProxy
	out, _ := build(t, Input{State: st, ActiveProfile: entry})
	p, err := xray.ParseProfile(out.Config)
	if err != nil {
		t.Fatal(err)
	}
	engine := xray.New("")
	defer engine.Close()
	ctx := context.Background()
	if err := engine.Apply(ctx, p); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		host string
		port uint32
		want string
	}{
		{"site.example", 443, "/healthy"},
		{"blocked.example", 443, "/entry"},
		{"site.example", 80, "/entry"},
	} {
		route, err := engine.TestRoute(ctx, xray.RouteQuery{Domain: tc.host, Port: tc.port})
		if err != nil || !strings.HasSuffix(route.OutboundTag, tc.want) {
			t.Fatalf("%s:%d selected %+v, %v, want %s", tc.host, tc.port, route, err, tc.want)
		}
	}

	cyclic := *entry
	cyclic.Routing = json.RawMessage(strings.Replace(string(entry.Routing),
		`{"inboundTag":["next"],"balancerTag":"second"}`,
		`{"inboundTag":["next"],"outboundTag":"retry"}`, 1))
	out, _ = build(t, Input{State: st, ActiveProfile: &cyclic})
	p, err = xray.ParseProfile(out.Config)
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Apply(ctx, p); err != nil {
		t.Fatal(err)
	}
	route, err := engine.TestRoute(ctx, xray.RouteQuery{Domain: "site.example", Port: 443})
	if err != nil || route.OutboundTag != "block" {
		t.Fatalf("cyclic loopback selected %+v, %v, want block", route, err)
	}

}

func TestProfileRoutingReplacementPreservesExactDirectFallback(t *testing.T) {
	st := testState(t)
	st.Settings.Fallback = store.FallbackDirect
	if err := st.AddOutbound("direct-backup", []byte(`{"protocol":"blackhole"}`)); err != nil {
		t.Fatal(err)
	}
	profile := &subs.OutboundProfile{
		Outbounds: []json.RawMessage{json.RawMessage(`{"tag":"node","protocol":"freedom"}`)},
		Routing: json.RawMessage(`{"rules":[{"network":"tcp,udp","balancerTag":"pool"}],
			"balancers":[{"tag":"pool","selector":["node"]}]}`),
	}
	engine := xray.New("")
	defer engine.Close()
	ctx := context.Background()
	apply := func(state store.State) {
		t.Helper()
		out, _ := build(t, Input{State: state, ActiveProfile: profile})
		p, err := xray.ParseProfile(out.Config)
		if err != nil {
			t.Fatal(err)
		}
		if err := engine.Apply(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	apply(st)
	st.Rules = []store.Rule{{Tag: "block-domain", Match: json.RawMessage(`{"domain":["full:blocked.example"]}`), Target: store.Target{Kind: store.TargetBlock}}}
	apply(st)
	for range 32 {
		route, err := engine.TestRoute(ctx, xray.RouteQuery{Domain: "other.example", Port: 443})
		if err != nil || route.OutboundTag != "direct" {
			t.Fatalf("replacement fallback = %+v, %v", route, err)
		}
	}
}

func TestProfileCompositionValidatesRoutingInputs(t *testing.T) {
	outbound := json.RawMessage(`{"tag":"node","protocol":"freedom","StreamSettings":{"Sockopt":{"DialerProxy":"helper"}}}`)
	profile := &subs.OutboundProfile{
		Outbounds:   []json.RawMessage{outbound, json.RawMessage(`{"tag":"helper","protocol":"freedom"}`)},
		Observatory: json.RawMessage(`{"subjectSelector":["node"],"probeUrl":"https://probe.example"}`),
		Routing: json.RawMessage(`{"rules":[{"protocol":["http"],"balancerTag":"pool"}],
			"balancers":[{"tag":"pool","selector":["node"]}]}`),
	}
	out, c := build(t, Input{State: testState(t), Proxy: true, ActiveProfile: profile})
	if len(c.Inbounds) == 0 || c.Inbounds[0]["sniffing"] == nil {
		t.Fatal("provider protocol rule requires mixed-inbound sniffing")
	}
	if !strings.Contains(string(out.Config), `"dialerProxy":"masir-profile/`) {
		t.Fatal("profile dialer reference was not scoped")
	}
	profile.Routing = json.RawMessage(strings.Replace(string(profile.Routing), `"protocol":["http"]`, `"domains":["full:site.example"]`, 1))
	_, c = build(t, Input{State: testState(t), Proxy: true, ActiveProfile: profile})
	if len(c.Inbounds) == 0 || c.Inbounds[0]["sniffing"] == nil {
		t.Fatal("provider domain rule requires mixed-inbound sniffing")
	}
	profile.Observatory = json.RawMessage(`{"subjectSelector":["node"],"probeUrl":"invalid"}`)
	if _, err := Build(Input{State: testState(t), ActiveProfile: profile}); err == nil {
		t.Fatal("invalid observer URL was accepted")
	}
}

func TestKillSwitchBlocksMissingProxyAndKeepsDirectRules(t *testing.T) {
	st := store.State{Settings: store.DefaultSettings()}
	st.Settings.KillSwitch = true
	for tag, target := range map[string]store.Target{
		"vpn":    {Kind: store.TargetProxy},
		"pinned": {Kind: store.TargetConfig, SubscriptionID: "feed", ConfigID: "removed"},
		"direct": {Kind: store.TargetDirect},
	} {
		if err := st.AddRule(tag, []byte(`{"domain":["full:`+tag+`.example"]}`), target, false); err != nil {
			t.Fatal(err)
		}
	}
	out, cfg := build(t, Input{State: st})
	if _, err := xray.ParseProfile(out.Config); err != nil {
		t.Fatal(err)
	}
	if got := fallbackSelector(cfg); !reflect.DeepEqual(got, []any{"block"}) {
		t.Fatalf("default tunnel route = %v, want block", got)
	}
	if got := cfg.Outbounds[0]["tag"]; got != "block" {
		t.Fatalf("first outbound = %v, want block", got)
	}
	for _, tag := range []string{"vpn", "pinned", "masir-dns-upstream"} {
		if got := ruleByTag(cfg, tag)["outboundTag"]; got != "block" {
			t.Errorf("%s = %v, want block", tag, got)
		}
	}
	if got := ruleByTag(cfg, "direct")["outboundTag"]; got != "direct" {
		t.Errorf("direct rule = %v, want direct", got)
	}
	for _, outbound := range cfg.Outbounds {
		if outbound["tag"] != "dns-out" {
			continue
		}
		rules := outbound["settings"].(map[string]any)["rules"].([]any)
		if got := rules[1].(map[string]any)["action"]; got != "return" {
			t.Errorf("DNS record types other than A/AAAA = %v, want return", got)
		}
	}
}

func TestKillSwitchBlocksProviderDirectFallback(t *testing.T) {
	st := store.State{Settings: store.DefaultSettings()}
	st.Settings.KillSwitch = true
	profile := &subs.OutboundProfile{
		Outbounds: []json.RawMessage{
			json.RawMessage(`{"tag":"provider-direct","protocol":"freedom"}`),
			json.RawMessage(`{"tag":"relay","protocol":"vless","settings":{"address":"relay.example","port":443,"id":"b3deefe0-cf65-4c90-84bf-750d5ec07c08","encryption":"none"},"streamSettings":{"security":"tls"}}`),
		},
		Routing: json.RawMessage(`{"rules":[{"network":"tcp,udp","balancerTag":"pool"}],
			"balancers":[{"tag":"pool","selector":["relay"],"fallbackTag":"provider-direct"}]}`),
		DefaultBalancer: "pool",
	}
	out, cfg := build(t, Input{State: st, ActiveProfile: profile})
	if _, err := xray.ParseProfile(out.Config); err != nil {
		t.Fatal(err)
	}
	for _, outbound := range cfg.Outbounds {
		if strings.HasSuffix(outbound["tag"].(string), "/provider-direct") {
			if got := outbound["protocol"]; got != "blackhole" {
				t.Errorf("provider direct fallback = %v, want blackhole", got)
			}
			return
		}
	}
	t.Fatal("provider fallback is missing")
}
