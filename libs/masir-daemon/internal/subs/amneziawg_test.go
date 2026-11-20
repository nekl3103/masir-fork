package subs

import (
	"encoding/base64"
	"testing"
)

const amneziaConf = `[Interface]
PrivateKey = AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE=
Address = 10.11.0.2/32
Jc = 4
Jmin = 40
Jmax = 70
[Peer]
PublicKey = AgICAgICAgICAgICAgICAgICAgICAgICAgICAgICAgI=
Endpoint = awg.example.test:51820
AllowedIPs = 0.0.0.0/0
PersistentKeepalive = 25`

func TestParseMixedAmneziaSubscription(t *testing.T) {
	awglink := "vpn://" + base64.RawURLEncoding.EncodeToString([]byte(amneziaConf)) + "#Amnezia%20WG"
	vless := "vless://11111111-1111-4111-8111-111111111111@127.0.0.1:443?type=tcp&encryption=none#VLESS"
	body := base64.StdEncoding.EncodeToString([]byte(vless + "\n" + awglink + "\nvpn://broken"))
	nodes, err := Parse([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 2 {
		t.Fatalf("got %d nodes, want both VLESS and AmneziaWG", len(nodes))
	}
	var wantID string
	for _, node := range nodes {
		if node.Protocol != "amneziawg" {
			continue
		}
		if node.Name != "Amnezia WG" || node.Address != "awg.example.test" || node.Port != 51820 || node.Network != "udp" {
			t.Fatalf("unexpected AmneziaWG node metadata")
		}
		wantID = node.ID
	}
	nodes, err = Parse([]byte(amneziaConf))
	if err != nil || len(nodes) != 1 || nodes[0].ID != wantID {
		t.Fatalf("the link and the raw configuration should yield the same node: %v", err)
	}
}
