package subs

import (
	"encoding/base64"
	"fmt"
	"testing"
)

func TestParseMasqueJSONSubscription(t *testing.T) {
	for _, alpn := range []string{"h2", "h3"} {
		t.Run(alpn, func(t *testing.T) {
			doc := fmt.Sprintf(`[{
				"remarks": "MASQUE test",
				"outbounds": [
					{
						"tag": "proxy",
						"protocol": "masque",
						"settings": {"address":"vpn.example.test","port":11443},
						"streamSettings": {
							"network":"masque","security":"tls",
							"tlsSettings":{"serverName":"vpn.example.test","alpn":[%q]},
							"masqueSettings":{"path":"/connect-ip","user":"fixture@example.test","pass":"example"}
						}
					},
					{"tag":"direct","protocol":"freedom"}
				]
			}]`, alpn)
			for _, body := range []string{doc, base64.StdEncoding.EncodeToString([]byte(doc))} {
				configs, err := Parse([]byte(body))
				if err != nil || len(configs) != 1 {
					t.Fatalf("MASQUE import: got %d nodes, err=%v", len(configs), err)
				}
				c := configs[0]
				if c.Name != "MASQUE test" || c.Protocol != "masque" || c.Address != "vpn.example.test" ||
					c.Port != 11443 || c.Network != "masque" || c.Security != "tls" {
					t.Fatal("MASQUE subscription node metadata was not preserved")
				}
			}
		})
	}
}
