package xray

import (
	"encoding/json"
	"net"
	"slices"
	"strings"

	"github.com/Gozargah/Masir/libs/masir-daemon/internal/amneziawg"
)

// BootstrapDomains lists the hostnames (not IPs) these outbounds dial, as full: matchers.
func BootstrapDomains(outbounds []json.RawMessage) []string {
	var out []string
	seen := map[string]bool{}
	for _, raw := range outbounds {
		for _, h := range serverHosts(raw) {
			if net.ParseIP(h) != nil || seen[h] {
				continue
			}
			seen[h] = true
			out = append(out, "full:"+h)
		}
	}
	return out
}

// serverHosts covers the shapes Xray accepts: flat settings.address, vnext, servers,
// and wireguard peers[].endpoint (whose address is the tunnel IP, not a server).
func serverHosts(raw json.RawMessage) []string {
	var o struct {
		Protocol string `json:"protocol"`
		Settings struct {
			Address any `json:"address"`
			Vnext   []struct {
				Address string `json:"address"`
			} `json:"vnext"`
			Servers []struct {
				Address string `json:"address"`
			} `json:"servers"`
			Peers []struct {
				Endpoint string `json:"endpoint"`
			} `json:"peers"`
		} `json:"settings"`
	}
	if json.Unmarshal(raw, &o) != nil {
		return nil
	}
	var hosts []string
	if !strings.EqualFold(o.Protocol, "wireguard") && !strings.EqualFold(o.Protocol, amneziawg.Protocol) {
		switch a := o.Settings.Address.(type) {
		case string:
			hosts = append(hosts, a)
		case []any:
			for _, v := range a {
				if s, ok := v.(string); ok {
					hosts = append(hosts, s)
				}
			}
		}
	}
	for _, v := range o.Settings.Vnext {
		hosts = append(hosts, v.Address)
	}
	for _, s := range o.Settings.Servers {
		hosts = append(hosts, s.Address)
	}
	for _, p := range o.Settings.Peers {
		if h, _, err := net.SplitHostPort(p.Endpoint); err == nil {
			hosts = append(hosts, h)
		}
	}
	return slices.DeleteFunc(hosts, func(h string) bool { return strings.TrimSpace(h) == "" })
}
