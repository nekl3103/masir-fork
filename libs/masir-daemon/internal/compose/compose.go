// Package compose builds the full Xray config from masird's state. DNS is captured
// inside Xray rather than by changing system settings, and fallback is the last
// routing rule, pointing at a balancer the daemon can retarget without a rewrite.
package compose

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"

	"github.com/Gozargah/Masir/libs/masir-daemon/internal/store"
	"github.com/Gozargah/Masir/libs/masir-daemon/internal/subs"
	"github.com/Gozargah/Masir/libs/masir-daemon/internal/xray"
)

const (
	ProxyInboundTag = "masir-proxy"
	TunInboundTag   = "tun-in"
	proxyTag        = "proxy"

	// FallbackBalancer is the balancer the catch-all rule points at. Its selector names
	// the outbound the fallback currently takes, and the daemon switches modes by
	// overriding that target: one atomic call, so no rule or TUN has to be rebuilt.
	FallbackBalancer = "masir-fallback"
)

// privateRanges stands in for geoip:private so the config builds without geoip.dat.
var privateRanges = []string{
	"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16",
	"172.16.0.0/12", "192.168.0.0/16", "224.0.0.0/4", "255.255.255.255/32",
	"::1/128", "fc00::/7", "fe80::/10", "ff00::/8",
}

type Input struct {
	State                store.State
	Proxy                bool            // the mixed proxy inbound
	Tun                  bool            // the TUN inbound
	TunName              string          // interface name; empty lets Xray pick
	LogDir               string          // where Xray writes its error log; empty keeps it on stdout
	Active               json.RawMessage // the selected config's outbound; nil when nothing is selected
	ActiveProfile        *subs.OutboundProfile
	ActiveID             string // empty for callers that do not persist a selected config
	ActiveSubscriptionID string
	// Skip names rules left out of this config without being disabled: the daemon
	// suspends one it can't verify (see server.gate), and brings it back once it can.
	Skip map[string]bool
	// Resolve finds a pinned config, following it by name when its ID changed upstream.
	Resolve func(store.Target) (subs.Config, bool)
}

// ResolvedRule says where a user rule's traffic actually goes.
type ResolvedRule struct {
	Rule        store.Rule
	OutboundTag string
	FellBack    bool // the target couldn't be used, so OutboundTag is the fallback
}

func (r *ResolvedRule) setTarget(t profileTarget) {
	r.OutboundTag = t.tag
}

type Output struct {
	Config []byte
	Rules  []ResolvedRule
}

func Build(in Input) (Output, error) {
	st := in.State
	hasProxy := len(in.Active) > 0 || in.ActiveProfile != nil
	proxy := profileTarget{tag: "direct"}
	graph := profileGraph{killSwitch: st.Settings.KillSwitch}
	if st.Settings.KillSwitch && !hasProxy {
		proxy.tag = "block"
	}
	if hasProxy {
		proxy.tag = proxyTag
	}

	// Outbounds: built-ins, the selection, pinned configs, custom ones, then DNS plumbing.
	outbounds := []any{
		obj{"tag": "direct", "protocol": "freedom"},
		obj{"tag": "block", "protocol": "blackhole"},
	}
	if st.Settings.KillSwitch {
		// Xray can use the first outbound if it finds no route or handler.
		outbounds[0], outbounds[1] = outbounds[1], outbounds[0]
	}
	serverOutbounds := []json.RawMessage{}
	if in.ActiveProfile != nil {
		var members []json.RawMessage
		revision, err := json.Marshal(in.ActiveProfile)
		if err != nil {
			return Output{}, fmt.Errorf("selected profile revision: %w", err)
		}
		digest := sha256.Sum256(revision)
		namespace := fmt.Sprintf("masir-profile/active-%x", digest[:8])
		if in.ActiveSubscriptionID != "" && in.ActiveID != "" {
			namespace = fmt.Sprintf("masir-profile/%s/%s/active-%x", in.ActiveSubscriptionID, in.ActiveID, digest[:8])
		}
		proxy, members, err = graph.add(in.ActiveProfile, namespace)
		if err != nil {
			return Output{}, fmt.Errorf("selected config: %w", err)
		}
		for _, member := range members {
			outbounds = append(outbounds, member)
		}
		serverOutbounds = append(serverOutbounds, members...)
	} else if hasProxy {
		o, err := withTag(in.Active, proxy.tag, st.Settings.KillSwitch)
		if err != nil {
			return Output{}, fmt.Errorf("selected config: %w", err)
		}
		outbounds = append(outbounds, o)
		serverOutbounds = append(serverOutbounds, in.Active)
	}

	var resolved []ResolvedRule
	pinned := map[string]profileTarget{}
	for _, r := range st.Rules {
		if !st.Settings.RulesEnabled || r.Disabled || in.Skip[r.Tag] {
			continue
		}
		rr := ResolvedRule{Rule: r}
		switch r.Target.Kind {
		case store.TargetDirect, store.TargetBlock:
			rr.OutboundTag = string(r.Target.Kind)
		case store.TargetProxy:
			rr.setTarget(proxy)
			rr.FellBack = !hasProxy
		case store.TargetOutbound:
			rr.OutboundTag = r.Target.Tag
		case store.TargetConfig:
			c, ok := subs.Config{}, false
			if in.Resolve != nil {
				c, ok = in.Resolve(r.Target)
			}
			if !ok {
				rr.setTarget(proxy)
				rr.FellBack = true
				break
			}
			tag := "sub-" + r.Target.SubscriptionID + "-" + c.ID
			target, exists := pinned[tag]
			if !exists {
				if c.Profile != nil {
					balancer, members, err := graph.add(c.Profile, "masir-profile/"+r.Target.SubscriptionID+"/"+c.ID+"/pinned")
					if err != nil {
						return Output{}, fmt.Errorf("pinned config %s: %w", c.Name, err)
					}
					for _, member := range members {
						outbounds = append(outbounds, member)
					}
					serverOutbounds = append(serverOutbounds, members...)
					target = balancer
				} else {
					o, err := withTag(c.Outbound, tag, st.Settings.KillSwitch)
					if err != nil {
						return Output{}, fmt.Errorf("pinned config %s: %w", c.Name, err)
					}
					outbounds = append(outbounds, o)
					serverOutbounds = append(serverOutbounds, c.Outbound)
					target = profileTarget{tag: tag}
				}
				pinned[tag] = target
			}
			rr.setTarget(target)
		default:
			return Output{}, fmt.Errorf("rule %q has unknown target %q", r.Tag, r.Target.Kind)
		}
		resolved = append(resolved, rr)
	}
	for _, it := range st.Outbounds {
		outbounds = append(outbounds, it.Config)
		serverOutbounds = append(serverOutbounds, it.Config)
	}
	// Xray can resolve A and AAAA through DoH. Its DNS proxy otherwise forwards
	// other record types in plaintext, which the Kill Switch must prevent.
	dnsOther := "direct"
	if st.Settings.KillSwitch {
		dnsOther = "return"
	}
	outbounds = append(outbounds, obj{
		"tag": "dns-out", "protocol": "dns",
		"settings": obj{"rules": []obj{{"action": "hijack", "qType": "1,28"}, {"action": dnsOther}}},
	})

	// Inbounds.
	var inbounds []any
	if in.Proxy {
		listen := "127.0.0.1"
		if st.Settings.AllowLAN {
			listen = "0.0.0.0"
		}
		mixed := obj{
			"tag": ProxyInboundTag, "listen": listen, "port": st.Settings.ProxyPort, "protocol": "mixed",
			"settings": obj{"auth": "noauth", "udp": true},
		}
		// Sniff when a user or provider rule can use the result.
		if len(st.Rules) > 0 || graph.sniff {
			mixed["sniffing"] = sniffing(false)
		}
		inbounds = append(inbounds, mixed)
	}
	if in.Tun {
		tunDNS := st.Settings.TunDNS
		if len(tunDNS) == 0 {
			tunDNS = st.Settings.DNSServers
		}
		gateways := []string{st.Settings.TunGateway}
		if st.Settings.TunGateway != "" {
			// Provide an IPv6 ULA gateway prefix to match the ::/0 routing in
			// autoSystemRoutingTable and ensure IPv6 traffic has a valid interface address.
			gateways = append(gateways, "fd00::1/64")
		}
		settings := obj{
			"autoOutboundsInterface": "auto",
			"autoSystemRoutingTable": []string{"0.0.0.0/0", "::/0"},
			"dns":                    tunDNS,
			"gateway":                gateways,
			"mtu":                    1500,
		}
		if in.TunName != "" {
			settings["name"] = in.TunName
		}
		switch goos {
		case "windows":
			// Windows also sends lookups to the other adapters' DNS servers, bypassing the
			// TUN; Xray's filter keeps port 53 inside it, where it's resolved through Xray.
			// "misconfigtun" blocks traffic for unrouted or misconfigured IP versions.
			settings["autoSystemWfpBlockLeak"] = []string{"dns", "misconfigtun"}
			settings["desc"] = "Masir"
		case "linux", "freebsd":
			// Points systemd-resolved (via resolvectl) or resolvconf to the TUN gateway,
			// preventing system DNS queries from leaking outside the TUN interface.
			settings["autoSystemDnsToGateway"] = true
		}
		inbounds = append(inbounds, obj{"tag": TunInboundTag, "protocol": "tun", "settings": settings, "sniffing": sniffing(true)})
	}
	for _, it := range st.Inbounds {
		inbounds = append(inbounds, it.Config)
	}

	// DNS: everything through DoH over the proxy; proxy server names bootstrap over plain DNS, direct.
	doh := parseDoH(st.Settings.DoHDNS)
	domains := xray.BootstrapDomains(serverOutbounds)
	if !doh.isIP && !slices.Contains(domains, "full:"+doh.host) {
		domains = append(domains, "full:"+doh.host)
	}
	dnsServers := []any{obj{"address": st.Settings.DoHDNS}}
	for _, s := range st.Settings.DNSServers {
		// Bootstrap is only for these names, never a direct fallback for DoH.
		dnsServers = append(dnsServers, obj{"address": s, "domains": domains, "skipFallback": true, "finalQuery": true, "tag": "dns-bootstrap"})
	}

	// Rules: plumbing first, so no user rule can break DNS or loop Xray; then user rules;
	// then the LAN bypass; then fallback.
	upstream := obj{"ruleTag": "masir-dns-upstream", "port": doh.port, "outboundTag": proxy.tag}
	if doh.isIP {
		upstream["ip"] = []string{doh.host}
	} else {
		upstream["domain"] = []string{"full:" + doh.host}
	}
	rules := []any{
		obj{"ruleTag": "masir-anti-loop", "process": []string{"self/", "xray/"}, "outboundTag": "direct"},
		obj{"ruleTag": "masir-dns-bootstrap", "inboundTag": []string{"dns-bootstrap"}, "outboundTag": "direct"},
		obj{"ruleTag": "masir-dns-hijack", "port": 53, "network": "tcp,udp", "outboundTag": "dns-out"},
		upstream,
	}
	userIPRules := false
	for _, rr := range resolved {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(rr.Rule.Match, &m); err != nil {
			return Output{}, fmt.Errorf("rule %q: %w", rr.Rule.Tag, err)
		}
		if _, ok := m["ip"]; ok {
			userIPRules = true
		}
		m["ruleTag"], _ = json.Marshal(rr.Rule.Tag)
		m["outboundTag"], _ = json.Marshal(rr.OutboundTag)
		rules = append(rules, m)
	}
	fallback := "direct"
	if st.Settings.Fallback == store.FallbackProxy && (hasProxy || st.Settings.KillSwitch) {
		fallback = proxy.tag
	}
	if proxy.scoped {
		fallback = "direct"
	}
	defaultRoute := obj{"ruleTag": "masir-default-route", "network": "tcp,udp", "balancerTag": FallbackBalancer}
	if st.Settings.Fallback == store.FallbackProxy && proxy.scoped {
		delete(defaultRoute, "balancerTag")
		defaultRoute["outboundTag"] = proxy.tag
	}
	rules = append(rules,
		obj{"ruleTag": "masir-lan-bypass", "ip": privateRanges, "outboundTag": "direct"},
		defaultRoute,
	)

	// IP rules only see IP destinations under AsIs; resolve unmatched domains so they apply too.
	strategy := "AsIs"
	if userIPRules {
		strategy = "IPIfNonMatch"
	}

	level := store.NormalizeXrayLogLevel(st.Settings.XrayLogLevel)
	if level == "" {
		level = "warning"
	}
	log := obj{"loglevel": level}
	if in.LogDir != "" {
		// A file log lets the daemon tee Xray's own warnings into `masir logs` (see
		// xray.SetLogSink) without also capturing temporary latency-test instances.
		log["error"] = filepath.Join(in.LogDir, "xray-error.log")
	}
	config := obj{
		"log": log,
		// Apply the IPv4 policy to every resolver, including bootstrap servers.
		"dns":       obj{"servers": dnsServers, "queryStrategy": "UseIPv4", "tag": "masir-dns"},
		"inbounds":  orEmpty(inbounds),
		"outbounds": outbounds,
		"routing": obj{
			"domainStrategy": strategy,
			"balancers":      []any{obj{"tag": FallbackBalancer, "selector": []string{fallback}}},
			"rules":          rules,
		},
	}
	graph.applyObservers(config)
	if len(graph.scopes) > 0 {
		config["masirProfileRoutes"] = graph.scopes
	}
	cfg, err := json.Marshal(config)
	if err != nil {
		return Output{}, err
	}
	return Output{Config: cfg, Rules: resolved}, nil
}

// goos is runtime.GOOS; tests set it to build another system's config.
var goos = runtime.GOOS

// TunName returns a free utunN on macOS, the only names it allows; elsewhere Xray picks.
func TunName() string {
	if runtime.GOOS != "darwin" {
		return ""
	}
	used := map[string]bool{}
	if ifs, err := net.Interfaces(); err == nil {
		for _, i := range ifs {
			used[i.Name] = true
		}
	}
	for n := 10; ; n++ { // macOS itself takes the low numbers
		if name := "utun" + strconv.Itoa(n); !used[name] {
			return name
		}
	}
}

type obj = map[string]any

func sniffing(fakedns bool) obj {
	dest := []string{"http", "tls", "quic"}
	if fakedns {
		dest = append(dest, "fakedns")
	}
	// routeOnly: the sniffed name only feeds routing; the destination is never rewritten.
	return obj{"enabled": true, "destOverride": dest, "routeOnly": true}
}

func withTag(raw json.RawMessage, tag string, killSwitch bool) (json.RawMessage, error) {
	var o map[string]json.RawMessage
	if err := json.Unmarshal(raw, &o); err != nil {
		return nil, err
	}
	stripHysteriaServerMasquerade(o)
	if killSwitch {
		o = blockProviderDirect(o)
	}
	o["tag"], _ = json.Marshal(tag)
	return json.Marshal(o)
}

func orEmpty(v []any) []any {
	if v == nil {
		return []any{}
	}
	return v
}

type dohEndpoint struct {
	host string
	isIP bool
	port int
}

func parseDoH(raw string) dohEndpoint {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return dohEndpoint{host: "1.1.1.1", isIP: true, port: 443}
	}
	port := 443
	if p, err := strconv.Atoi(u.Port()); err == nil {
		port = p
	}
	return dohEndpoint{host: u.Hostname(), isIP: net.ParseIP(u.Hostname()) != nil, port: port}
}
