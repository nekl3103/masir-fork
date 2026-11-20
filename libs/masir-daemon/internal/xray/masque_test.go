package xray

import (
	"context"
	"fmt"
	"testing"
)

func TestMasqueAndDirectRoutes(t *testing.T) {
	for _, alpn := range []string{"h2", "h3"} {
		t.Run(alpn, func(t *testing.T) {
			raw := fmt.Appendf(nil, `{
				"outbounds": [
					{"protocol":"freedom","tag":"direct"},
					{
						"protocol":"masque","tag":"vpn",
						"settings":{"address":"vpn.example.test","port":11443},
						"streamSettings":{
							"network":"masque","security":"tls",
							"tlsSettings":{"serverName":"vpn.example.test","alpn":[%q]},
							"masqueSettings":{"path":"/connect-ip","user":"fixture@example.test","pass":"example"}
						}
					}
				],
				"routing":{"rules":[{"type":"field","domain":["full:example.test"],"outboundTag":"vpn"}]}
			}`, alpn)
			p, err := ParseProfile(raw)
			if err != nil {
				t.Fatal(err)
			}
			core := New("")
			defer core.Close()
			if err := core.Apply(context.Background(), p); err != nil {
				t.Fatal(err)
			}
			for _, tc := range []struct{ domain, tag string }{
				{"example.test", "vpn"},
				{"another.test", "direct"},
			} {
				route, err := core.TestRoute(context.Background(), RouteQuery{Domain: tc.domain, Port: 443})
				if err != nil || route.OutboundTag != tc.tag {
					t.Fatalf("route for %s: got %+v, err=%v; want %s", tc.domain, route, err, tc.tag)
				}
			}
		})
	}
}

func TestRejectIncompatibleMasqueSettings(t *testing.T) {
	for _, partial := range []string{
		`"streamSettings":{"network":"masque"}`,
		`"streamSettings":{"network":"tcp","security":"tls"}`,
		`"streamSettings":{"network":"masque","security":"tls"},"mux":{"enabled":true}`,
	} {
		outbound := fmt.Appendf(nil, `{"protocol":"masque","settings":{"address":"vpn.example.test","port":443},%s}`, partial)
		if err := ValidateOutbound(outbound); err == nil {
			t.Fatal("accepted an incompatible MASQUE outbound")
		}
	}
}
