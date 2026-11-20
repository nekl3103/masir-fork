package xray

import (
	"testing"

	"github.com/xtls/xray-core/core"
)

func TestTUICBuildsWithXrayRouting(t *testing.T) {
	raw := []byte(`{
		"outbounds": [
			{"protocol":"freedom","tag":"direct"},
			{"protocol":"tuic","tag":"vpn","settings":{
				"address":"127.0.0.1","port":443,
				"uuid":"11111111-1111-4111-8111-111111111111","password":"test"
			}}
		],
		"routing":{"rules":[{"type":"field","domain":["full:example.test"],"outboundTag":"vpn"}]}
	}`)
	cfg, err := loadConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	instance, err := core.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer instance.Close()
	if err := instance.Start(); err != nil {
		t.Fatal(err)
	}
}
