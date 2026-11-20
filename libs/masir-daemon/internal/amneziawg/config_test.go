package amneziawg

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"strings"
	"testing"
)

const testConf = `# Exported AmneziaWG profile
[Interface]
Address = 10.66.0.2/32, fd00::2/128
PrivateKey = AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE=
DNS = $PRIMARY_DNS, $SECONDARY_DNS
Jc = 4
Jmin = 10
Jmax = 50
H1 = 100-199
I1 = <b 0x1234><r 16>
S1 = 24
S2 = 24
S3 = 24
S4 = 24
RandomTrailers = on
DisableCookies = off

[Peer]
PublicKey = AgICAgICAgICAgICAgICAgICAgICAgICAgICAgICAgI=
AllowedIPs = 0.0.0.0/0, ::/0
Endpoint = [2001:db8::1]:51820
PersistentKeepalive = 25-30
`

func TestAmneziaExport(t *testing.T) {
	bundle, err := json.Marshal(map[string]any{
		"description": "Amnezia",
		"dns1":        "1.1.1.1",
		"dns2":        "2606:4700:4700::1111",
		"containers": []any{map[string]any{"awg": map[string]any{
			"last_config": map[string]any{"config": testConf, "mtu": "1376"},
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var frame bytes.Buffer
	frame.Write(binary.BigEndian.AppendUint32(nil, uint32(len(bundle))))
	w := zlib.NewWriter(&frame)
	if _, err := w.Write(bundle); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	profiles, err := ParseLink("vpn://" + base64.RawURLEncoding.EncodeToString(frame.Bytes()))
	if err != nil || len(profiles) != 1 {
		t.Fatalf("ParseLink: profiles=%d, err=%v", len(profiles), err)
	}
	c := profiles[0].Config
	if profiles[0].Name != "Amnezia" || c.MTU != 1376 || c.DNS[0] != "1.1.1.1" || c.Peers[0].Endpoint != "[2001:db8::1]:51820" {
		t.Fatal("profile name, MTU, DNS or endpoint was not imported")
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	settings, err := c.UAPI()
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"random_trailers=true", "disable_cookies=false", "h1=100-199", "i1=<b 0x1234><r 16>", "allowed_ip=::/0", "persistent_keepalive_interval=25-30"} {
		if !strings.Contains(settings, field+"\n") {
			t.Errorf("expected %q", field)
		}
	}
}

func TestRejectInvalidAmneziaPacketLengths(t *testing.T) {
	for _, input := range []string{
		strings.Replace(testConf, "<r 16>", "<r -1>", 1),
		strings.Replace(testConf, "Jmax = 50", "Jmax = 4", 1),
		strings.Replace(testConf, "S4 = 24", "S4 = 65535", 1),
	} {
		c, err := ParseConfig(input)
		if err != nil {
			t.Fatal(err)
		}
		if err := c.Validate(); err == nil {
			t.Fatal("accepted an invalid packet length")
		}
	}
}
