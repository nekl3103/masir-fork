package compose

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/Gozargah/Masir/libs/masir-daemon/internal/subs"
	"github.com/xtls/xray-core/app/router"
	"github.com/xtls/xray-core/infra/conf"
)

type profileTarget struct {
	tag    string
	scoped bool
}

type profileGraph struct {
	observers []profileObserver
	scopes    map[string]obj
	sniff     bool
}

type profileObserver struct {
	kind      string
	settings  map[string]json.RawMessage
	selectors []string
}

func (g *profileGraph) add(p *subs.OutboundProfile, namespace string) (profileTarget, []json.RawMessage, error) {
	var routing struct {
		Rules     []map[string]json.RawMessage `json:"rules"`
		Balancers []struct {
			Tag         string          `json:"tag"`
			Selector    conf.StringList `json:"selector"`
			FallbackTag string          `json:"fallbackTag"`
			Strategy    json.RawMessage `json:"strategy"`
		} `json:"balancers"`
	}
	if err := json.Unmarshal(p.Routing, &routing); err != nil {
		return profileTarget{}, nil, err
	}

	prefix := namespace + "/"
	outbounds := make([]json.RawMessage, 0, len(p.Outbounds))
	tags := make(map[string]bool, len(p.Outbounds))
	var tagOrder []string
	chains := make(map[string]string)
	loopbacks := make(map[string]string)
	var firstOutbound string
	for i, raw := range p.Outbounds {
		var outbound struct {
			Tag      string `json:"tag"`
			Protocol string `json:"protocol"`
		}
		if err := json.Unmarshal(raw, &outbound); err != nil {
			return profileTarget{}, nil, err
		}
		tag := prefix + outbound.Tag
		tags[outbound.Tag] = true
		if outbound.Tag != "" {
			tagOrder = append(tagOrder, outbound.Tag)
		}
		if outbound.Tag == "" {
			tag = fmt.Sprintf("%suntagged/%d", namespace, i)
		}
		var o map[string]json.RawMessage
		if err := json.Unmarshal(raw, &o); err != nil || o == nil {
			return profileTarget{}, nil, fmt.Errorf("invalid balanced profile outbound %d", i+1)
		}
		if err := canonicalFields(o, "tag", "protocol", "streamSettings", "settings"); err != nil {
			return profileTarget{}, nil, err
		}
		stripHysteriaServerMasquerade(o)
		if i == 0 {
			firstOutbound = tag
		}
		if strings.EqualFold(outbound.Protocol, "loopback") {
			var settings struct {
				InboundTag string `json:"inboundTag"`
				Sniffing   struct {
					Enabled bool `json:"enabled"`
				} `json:"sniffing"`
			}
			if err := json.Unmarshal(o["settings"], &settings); err != nil || settings.InboundTag == "" || settings.Sniffing.Enabled {
				return profileTarget{}, nil, fmt.Errorf("balanced profile has unsupported loopback settings")
			}
			loopbacks[tag] = settings.InboundTag
			// Loopback only re-enters the router. Resolve it within the profile
			// before dispatch, so the outer routing policy runs once per flow.
			o = map[string]json.RawMessage{"protocol": json.RawMessage(`"blackhole"`)}
		}

		// A dial chain must stay in the same provider namespace.
		if stream := o["streamSettings"]; len(stream) > 0 {
			var settings map[string]json.RawMessage
			if err := json.Unmarshal(stream, &settings); err != nil {
				return profileTarget{}, nil, err
			}
			if err := canonicalFields(settings, "sockopt"); err != nil {
				return profileTarget{}, nil, err
			}
			if rawSock := settings["sockopt"]; len(rawSock) > 0 {
				var socket map[string]json.RawMessage
				if err := json.Unmarshal(rawSock, &socket); err != nil {
					return profileTarget{}, nil, err
				}
				if err := canonicalFields(socket, "dialerProxy"); err != nil {
					return profileTarget{}, nil, err
				}
				var chain string
				if raw := socket["dialerProxy"]; len(raw) > 0 {
					if err := json.Unmarshal(raw, &chain); err != nil {
						return profileTarget{}, nil, err
					}
				}
				if chain != "" {
					chains[outbound.Tag] = chain
					socket["dialerProxy"], _ = json.Marshal(prefix + chain)
					settings["sockopt"], _ = json.Marshal(socket)
					o["streamSettings"], _ = json.Marshal(settings)
				}
			}
		}
		o["tag"], _ = json.Marshal(tag)
		encoded, err := json.Marshal(o)
		if err != nil {
			return profileTarget{}, nil, err
		}
		outbounds = append(outbounds, encoded)
	}

	for start := range chains {
		seen := map[string]bool{start: true}
		for hop := chains[start]; hop != ""; hop = chains[hop] {
			if !tags[hop] || seen[hop] || loopbacks[prefix+hop] != "" {
				return profileTarget{}, nil, fmt.Errorf("balanced profile has an invalid dialerProxy chain")
			}
			seen[hop] = true
		}
	}
	hasObserver := false
	for key, raw := range map[string]json.RawMessage{"observatory": p.Observatory, "burstObservatory": p.BurstObservatory} {
		if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
			continue
		}
		if hasObserver {
			return profileTarget{}, nil, fmt.Errorf("balanced profile configures both Xray observer types")
		}
		if err := g.addObserver(key, prefix, raw); err != nil {
			return profileTarget{}, nil, err
		}
		hasObserver = true
	}
	var scopeBalancers []any
	for _, balancer := range routing.Balancers {
		// Keep the balancer on the route from the first connection. Its
		// fallback carries traffic until the strategy has an observation.
		fallback := "block"
		if balancer.FallbackTag != "" {
			fallback = prefix + balancer.FallbackTag
		} else {
			for _, tag := range tagOrder {
				if matchesSelector(tag, balancer.Selector) {
					fallback = prefix + tag
					break
				}
			}
		}
		spec := obj{"tag": prefix + balancer.Tag, "selector": scopedSelectors(prefix, balancer.Selector), "fallbackTag": fallback}
		if len(balancer.Strategy) > 0 {
			var strategy struct {
				Type     string `json:"type"`
				Settings struct {
					Costs []*router.StrategyWeight `json:"costs"`
				} `json:"settings"`
			}
			if err := json.Unmarshal(balancer.Strategy, &strategy); err != nil {
				return profileTarget{}, nil, err
			}
			switch strings.ToLower(strategy.Type) {
			case "leastping", "leastload":
				if !hasObserver {
					return profileTarget{}, nil, fmt.Errorf("balanced profile strategy %s requires an observatory", strategy.Type)
				}
			}
			compiled, err := scopedStrategy(balancer.Strategy, strategy.Settings.Costs, prefix, tagOrder)
			if err != nil {
				return profileTarget{}, nil, err
			}
			spec["strategy"] = compiled
		}
		scopeBalancers = append(scopeBalancers, spec)
	}
	for _, rule := range routing.Rules {
		for key := range rule {
			switch strings.ToLower(key) {
			case "protocol", "domain", "domains":
				g.sniff = true
			}
		}
	}
	scopedRules, err := scopeRules(routing.Rules, p.Ingress, prefix, loopbacks)
	if err != nil {
		return profileTarget{}, nil, err
	}
	var policy struct {
		DomainStrategy string `json:"domainStrategy"`
		Script         string `json:"script"`
	}
	if err := json.Unmarshal(p.Routing, &policy); err != nil {
		return profileTarget{}, nil, err
	}
	if policy.Script != "" {
		return profileTarget{}, nil, fmt.Errorf("balanced profile routing cannot run scripts")
	}
	if g.scopes == nil {
		g.scopes = make(map[string]obj)
	}
	scopeTag := "masir-profile-route/" + prefix
	g.scopes[scopeTag] = obj{"rules": scopedRules, "balancers": scopeBalancers, "domainStrategy": policy.DomainStrategy,
		"firstOutbound": firstOutbound, "loopbacks": loopbacks}
	return profileTarget{tag: scopeTag, scoped: true}, outbounds, nil
}

func scopedSelectors(prefix string, refs []string) []string {
	out := make([]string, 0, len(refs))
	for _, ref := range refs {
		out = append(out, prefix+ref)
	}
	return out
}

func (g *profileGraph) addObserver(key, prefix string, raw json.RawMessage) error {
	var observer map[string]json.RawMessage
	if err := json.Unmarshal(raw, &observer); err != nil {
		return err
	}
	if err := canonicalFields(observer, "subjectSelector", "probeURL"); err != nil {
		return err
	}
	if key == "observatory" && len(observer["probeURL"]) > 0 {
		var probe string
		if err := json.Unmarshal(observer["probeURL"], &probe); err != nil {
			return fmt.Errorf("invalid observatory probe URL")
		}
		if probe != "" {
			u, err := url.Parse(probe)
			if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") {
				return fmt.Errorf("observatory probe URL must use HTTP or HTTPS")
			}
		}
	}
	var refs conf.StringList
	if err := json.Unmarshal(observer["subjectSelector"], &refs); err != nil {
		return fmt.Errorf("balanced profile %s requires a subjectSelector", key)
	}
	delete(observer, "subjectSelector")
	g.observers = append(g.observers, profileObserver{kind: key, settings: observer, selectors: scopedSelectors(prefix, refs)})
	return nil
}

func (g *profileGraph) applyObservers(config obj) {
	observers := make([]obj, 0, len(g.observers))
	for _, observer := range g.observers {
		settings := obj{"subjectSelector": observer.selectors}
		for key, value := range observer.settings {
			settings[key] = value
		}
		observers = append(observers, obj{observer.kind: settings})
	}
	switch len(observers) {
	case 0:
		config["observatory"] = obj{}
	case 1:
		for key, value := range observers[0] {
			config[key] = value
		}
	default:
		config["masirObservers"] = observers
	}
}

func matchesSelector(tag string, selectors []string) bool {
	for _, selector := range selectors {
		if strings.HasPrefix(tag, selector) {
			return true
		}
	}
	return false
}

// Evaluate provider costs in their original tag space. Match each resulting
// weight to the exact installed tag, leaving the rest of the strategy intact.
func scopedStrategy(raw json.RawMessage, costs []*router.StrategyWeight, prefix string, tags []string) (json.RawMessage, error) {
	if len(costs) == 0 {
		return raw, nil
	}
	for _, cost := range costs {
		if cost == nil {
			return nil, fmt.Errorf("balanced profile contains an empty strategy cost")
		}
	}
	weights := router.NewWeightManager(costs, 1, nil)
	scoped := make([]*router.StrategyWeight, 0, len(tags))
	for _, tag := range tags {
		weight := weights.Get(tag)
		if weight != 1 {
			scoped = append(scoped, &router.StrategyWeight{Regexp: true, Match: "^" + regexp.QuoteMeta(prefix+tag) + "$", Value: float32(weight)})
		}
	}
	var fields, settings map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(fields["settings"], &settings); err != nil {
		return nil, err
	}
	settings["costs"], _ = json.Marshal(scoped)
	fields["settings"], _ = json.Marshal(settings)
	return json.Marshal(fields)
}

// A provider may give its SOCKS and HTTP listeners different names. Masir's
// mixed ingress and TUN share a context, so only predicates covering every
// provider client ingress can be translated without guessing the protocol.
func scopeRules(rules []map[string]json.RawMessage, ingress []subs.ProfileIngress, prefix string, loopbacks map[string]string) ([]any, error) {
	aliases := make(map[string]bool)
	for _, in := range ingress {
		switch strings.ToLower(in.Protocol) {
		case "socks", "http", "mixed", "tun":
			if in.Tag != "" {
				aliases[in.Tag] = true
			}
		}
	}
	steps := make(map[string]bool, len(loopbacks))
	for _, step := range loopbacks {
		if aliases[step] {
			return nil, fmt.Errorf("balanced profile uses the same tag for a client ingress and a loopback")
		}
		steps[step] = false
	}

	out := make([]any, 0, len(rules)+1)
	for _, rule := range rules {
		for key, raw := range rule {
			switch strings.ToLower(key) {
			case "webhook":
				return nil, fmt.Errorf("balanced profile routing cannot run webhooks")
			case "outboundtag", "balancertag":
				var target string
				if err := json.Unmarshal(raw, &target); err != nil {
					return nil, err
				}
				if target != "" {
					rule[key], _ = json.Marshal(prefix + target)
				}
			case "inboundtag":
				var names conf.StringList
				if err := json.Unmarshal(raw, &names); err != nil {
					return nil, err
				}
				client := make(map[string]bool)
				var scoped []string
				for _, name := range names {
					if _, internal := steps[name]; internal {
						steps[name] = true
						scoped = append(scoped, prefix+"step/"+name)
					} else if aliases[name] {
						client[name] = true
					} else {
						return nil, fmt.Errorf("balanced profile has a rule for an unsupported ingress")
					}
				}
				if len(client) != 0 {
					if len(client) != len(aliases) {
						return nil, fmt.Errorf("balanced profile has a rule for an ambiguous client ingress")
					}
					scoped = append(scoped, prefix+"client")
				}
				rule[key], _ = json.Marshal(scoped)
			}
		}
		out = append(out, rule)
	}
	for step, referenced := range steps {
		if !referenced {
			return nil, fmt.Errorf("balanced profile loopback ingress %q has no routing rule", step)
		}
	}
	return out, nil
}

// canonicalFields matches the named fields using the same case folding as
// Xray and rejects ambiguous spellings before rewriting references.
func canonicalFields(object map[string]json.RawMessage, fields ...string) error {
	for _, field := range fields {
		var found string
		for key := range object {
			if strings.EqualFold(key, field) {
				if found != "" {
					return fmt.Errorf("balanced profile repeats the %s field", field)
				}
				found = key
			}
		}
		if found != "" && found != field {
			object[field] = object[found]
			delete(object, found)
		}
	}
	return nil
}
