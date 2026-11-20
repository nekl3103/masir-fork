package compose

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Gozargah/Masir/libs/masir-daemon/internal/store"
	"github.com/Gozargah/Masir/libs/masir-daemon/internal/subs"
	"github.com/Gozargah/Masir/libs/masir-daemon/internal/xray"
)

func TestHysteriaSubscriptionMasqueradeIsServerOnly(t *testing.T) {
	const client = `{"tag":"hy","protocol":"hysteria","settings":{"version":2,"address":"test.example","port":2443},
		"streamSettings":{"network":"hysteria","security":"tls","tlsSettings":{"serverName":"test.example"},
		"hysteriaSettings":{"version":2,"auth":"test-auth","udpIdleTimeout":60,"masquerade":{"type":"file","dir":"/var/www"}}}}`
	st := store.State{Settings: store.DefaultSettings()}
	st.Settings.Fallback = store.FallbackProxy
	if err := st.AddRule("pin", []byte(`{"domain":["full:pinned.test"]}`),
		store.Target{Kind: store.TargetConfig, SubscriptionID: "s1", ConfigID: "c1"}, false); err != nil {
		t.Fatal(err)
	}
	for _, variant := range []string{"active", "pinned", "balanced"} {
		t.Run(variant, func(t *testing.T) {
			in := Input{State: st}
			switch variant {
			case "active":
				in.Active = json.RawMessage(client)
			case "pinned":
				in.Resolve = func(store.Target) (subs.Config, bool) {
					return subs.Config{ID: "c1", Outbound: json.RawMessage(client)}, true
				}
			case "balanced":
				in.ActiveProfile = &subs.OutboundProfile{
					Outbounds:       []json.RawMessage{json.RawMessage(client)},
					Routing:         json.RawMessage(`{"rules":[{"network":"tcp,udp","balancerTag":"pick"}],"balancers":[{"tag":"pick","selector":["hy"]}]}`),
					DefaultBalancer: "pick",
				}
			}
			out, err := Build(in)
			if err != nil {
				t.Fatal(err)
			}
			if err := xray.CheckNoLocalFiles(out.Config); err != nil {
				t.Fatal(err)
			}
			cfg := string(out.Config)
			for _, field := range []string{`"auth":"test-auth"`, `"network":"hysteria"`, `"udpIdleTimeout":60`, `"serverName":"test.example"`} {
				if !strings.Contains(cfg, field) {
					t.Errorf("client field missing: %s", field)
				}
			}
			if strings.Contains(cfg, "masquerade") {
				t.Error("server-only masquerade remained in the client config")
			}
		})
	}
	t.Run("other local paths remain blocked", func(t *testing.T) {
		withKeyLog := strings.Replace(client, `"serverName"`, `"masterKeyLog":"/tmp/keys.log","serverName"`, 1)
		out, err := Build(Input{State: st, Active: json.RawMessage(withKeyLog)})
		if err != nil {
			t.Fatal(err)
		}
		if err := xray.CheckNoLocalFiles(out.Config); err == nil || !strings.Contains(err.Error(), "masterKeyLog") {
			t.Fatalf("expected key log path rejection, got %v", err)
		}
	})
}
