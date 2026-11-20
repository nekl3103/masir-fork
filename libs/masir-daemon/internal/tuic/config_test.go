package tuic

import "testing"

func TestParseSubscriptionLink(t *testing.T) {
	cfg, name, err := ParseLink("tuic://11111111-1111-4111-8111-111111111111:test%40pass@[2001:db8::1]:443?sni=vpn.example&udp_relay_mode=quic&congestion_control=bbr&alpn=h3#TUIC%20v5")
	if err != nil {
		t.Fatal(err)
	}
	if name != "TUIC v5" || cfg.Endpoint() != "[2001:db8::1]:443" || cfg.Password != "test@pass" ||
		cfg.ServerName != "vpn.example" || cfg.UDPRelayMode != "quic" || cfg.CongestionControl != "bbr" ||
		len(cfg.ALPN) != 1 || cfg.ALPN[0] != "h3" {
		t.Fatal("TUIC link parameters were not preserved")
	}
	if _, _, err := ParseLink("tuic://token@example.test:443"); err == nil {
		t.Fatal("accepted TUIC v4 credentials")
	}
}
