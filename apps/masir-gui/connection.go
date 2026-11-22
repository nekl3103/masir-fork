package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"time"

	pb "github.com/Gozargah/Masir/libs/masir-proto/gen/masir/daemon/v1"
	"github.com/egoist/mygo/ui"
)

type connectionState struct {
	generation                       uint64
	resultUntil                      time.Time
	testing, checked                 bool
	internetOK, proxyOK, proxyTested bool
	identity                         connectionIdentity
}

// A successful check only describes this connection; changing servers or routes
// invalidates it, including when an in-flight check finishes after the change.
type connectionIdentity struct {
	startedAt            int64
	sub, config, address string
	power, tun, proxy    bool
	fallback             pb.Fallback
}

func (a *app) connectionIdentity() connectionIdentity {
	st := a.status
	return connectionIdentity{st.GetStartedAtMs(), st.GetActiveSubscriptionId(), st.GetActiveConfigId(), st.GetProxyAddress(), st.GetSettings().GetPower(), st.GetTunRunning(), st.GetProxyRunning(), st.GetSettings().GetFallback()}
}

func connectionButton(c *ui.Context, key, text string, p palette, disabled bool) bool {
	b := ui.ButtonBase(c.Key(key)).Padding(6, 8).Radius(6).Background(p.subdued).Cursor(ui.CursorPointer).Disabled(disabled)
	b.Children(func() { label(c, text, sizeCaption, 16, 500, p.accent) })
	return b.Clicked() && !disabled
}

func (a *app) connectionWarnings(c *ui.Context, p palette) {
	for i, warning := range a.connectionHealth(time.Now()) {
		ui.Column(c.Key(fmt.Sprintf("connection-warning-%d", i))).Padding(8).Gap(4).Radius(cardRadius).Background(p.errorSurface).Children(func() {
			label(c, warning, sizeCaption, 15, 500, p.errorC)
		})
	}
}

func (a *app) connectionHealth(now time.Time) []string {
	var warnings []string
	if a.status.GetActiveStale() {
		warnings = append(warnings, tr("Selected server was removed. Its saved copy is still selected; choose another server."))
	}
	for _, sub := range a.subscriptions.subscriptions {
		if sub.Id != a.status.GetActiveSubscriptionId() {
			continue
		}
		u := sub.GetUsage()
		if u.GetExpireAtMs() > 0 && u.ExpireAtMs <= now.UnixMilli() {
			warnings = append(warnings, tr("Active subscription has expired."))
		}
		if u.GetTotalBytes() > 0 && (u.GetDownloadBytes() >= u.TotalBytes || u.GetUploadBytes() >= u.TotalBytes-u.GetDownloadBytes()) {
			warnings = append(warnings, tr("Active subscription has used all its traffic."))
		}
	}
	return warnings
}

func (a *app) connectionMessage() (string, bool) {
	s := a.connection
	switch {
	case !a.reachable:
		return tr("Waiting for daemon"), false
	case s.testing:
		return tr("Checking…"), false
	case !s.checked || s.identity != a.connectionIdentity():
		if !a.wantsOn() {
			return tr("Disconnected"), false
		}
		return tr("Connection not checked"), false
	case s.internetOK && s.proxyOK:
		return tr("Internet & proxy are working"), true
	case s.internetOK && !s.proxyTested:
		if a.connected() {
			return tr("Internet works · Proxy unavailable"), false
		}
		return tr("Internet is working"), true
	case s.internetOK:
		return tr("Internet works · Proxy failed"), false
	case s.proxyOK:
		return tr("Proxy works · Internet failed"), false
	default:
		return tr("Connection check failed"), false
	}
}

// Probes use isolated Xray instances. The proxy probe dials the actual local
// listener; the UI reports outcomes rather than timings or raw RPC errors.
func (a *app) testConnection() {
	if a.connection.testing || !a.reachable {
		return
	}
	identity := a.connectionIdentity()
	generation := a.connection.generation
	proxyConfig := localProxyProbe(a.status)
	a.connection.testing = true
	a.async(func() {
		probe := func(config string) bool {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			r, err := a.d.TestLatency(ctx, &pb.TestLatencyRequest{XrayConfigJson: config})
			return err == nil && r != nil && r.GetError() == ""
		}
		internetOK := probe(`{"outbounds":[{"protocol":"freedom"}]}`)
		proxyOK := false
		if proxyConfig != "" {
			proxyOK = probe(proxyConfig)
		}
		a.post(func() {
			a.connection.testing = false
			if generation != a.connection.generation || identity != a.connectionIdentity() || !a.reachable {
				return
			}
			hold := 3 * time.Second
			if !internetOK || !proxyOK {
				hold = 5 * time.Second
			}
			a.connection = connectionState{resultUntil: time.Now().Add(hold), generation: generation, checked: true, internetOK: internetOK, proxyOK: proxyOK, proxyTested: proxyConfig != "", identity: identity}
		})
	})
}

func localProxyProbe(st *pb.Status) string {
	if !st.GetProxyRunning() {
		return ""
	}
	host, portText, err := net.SplitHostPort(st.GetProxyAddress())
	if err != nil {
		return ""
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return ""
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	config := map[string]any{"outbounds": []any{map[string]any{"protocol": "socks", "settings": map[string]any{"servers": []any{map[string]any{"address": host, "port": port}}}}}}
	b, _ := json.Marshal(config)
	return string(b)
}

func (a *app) connectionResult() string {
	internet := tr("Internet failed")
	if a.connection.internetOK {
		internet = tr("Internet OK")
	}
	proxy := tr("Proxy failed")
	if !a.connection.proxyTested {
		proxy = tr("Proxy unavailable")
	} else if a.connection.proxyOK {
		proxy = tr("Proxy OK")
	}
	return internet + " · " + proxy
}
