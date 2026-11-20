package server

import (
	"context"
	"testing"

	"github.com/Gozargah/Masir/libs/masir-daemon/internal/xray"
	pb "github.com/Gozargah/Masir/libs/masir-proto/gen/masir/daemon/v1"
)

func TestDomainRouteChecksLiveXrayRule(t *testing.T) {
	ctx := context.Background()
	s := &Server{xray: xray.New("")}
	t.Cleanup(func() { _ = s.xray.Close() })
	profile, err := xray.ParseProfile([]byte(`{
		"outbounds": [{"protocol":"freedom","tag":"direct"},{"protocol":"blackhole","tag":"block"}],
		"routing":{"rules":[{"ruleTag":"blocked-by-test","domain":["full:example.com"],"outboundTag":"block"}]}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.xray.Apply(ctx, profile); err != nil {
		t.Fatal(err)
	}
	res, err := s.TestRouting(ctx, &pb.TestRoutingRequest{Domain: "ExAmPle.com.", ProbeEgress: true})
	if err != nil || !res.GetMatched() || res.GetRuleTag() != "blocked-by-test" || res.GetOutboundName() != "Blocked" {
		t.Fatalf("wrong route: %v, %v", res, err)
	}
	if res.GetHttpStatus() != 0 || res.GetHttpError() != "" || res.GetEgressError() != "" || res.GetEgressIp() != "" {
		t.Fatal("a blocked site should not trigger outbound network probes")
	}
	if _, err := s.TestRouting(ctx, &pb.TestRoutingRequest{Domain: "example.com@localhost", ProbeEgress: true}); err == nil {
		t.Fatal("malformed hostname accepted")
	}
}
