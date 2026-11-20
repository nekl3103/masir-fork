package subs

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type sourceTransport func(*http.Request) (*http.Response, error)

func (f sourceTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestSubscriptionGroupPreservesSources(t *testing.T) {
	ctx := context.Background()
	m := newTestManager(t, nil)
	const (
		rawURL    = "https://sub.example/raw"
		jsonURL   = "https://sub.example/json"
		tuicURL   = "tuic://00000000-0000-4000-8000-000000000001:test-password@tuic.example:10443#TUIC"
		vlessRaw  = "vless://b831381d-6324-4d53-ad4f-8cda48b30811@de.example.com:443?encryption=none&security=tls&sni=de.example.com&type=tcp#Raw"
		vlessJSON = `{"remarks":"JSON","routing":{"balancers":[{"tag":"pick","selector":["proxy"]}],"rules":[{"network":"tcp,udp","balancerTag":"pick"}]},"outbounds":[{"tag":"proxy","protocol":"vless","settings":{"address":"de.example.com","port":443,"id":"b831381d-6324-4d53-ad4f-8cda48b30811","level":8,"encryption":"none"},"streamSettings":{"network":"tcp","security":"tls","tlsSettings":{"serverName":"de.example.com"}}}]}`
	)
	bodies := map[string]string{rawURL: vlessRaw, jsonURL: vlessJSON}
	m.direct.Transport = sourceTransport(func(r *http.Request) (*http.Response, error) {
		body := bodies[r.URL.String()]
		code := http.StatusOK
		if body == "" {
			code = http.StatusServiceUnavailable
		}
		return &http.Response{StatusCode: code, Status: http.StatusText(code), Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})

	before, err := m.Add(ctx, jsonURL, "Panel", 0)
	if err != nil {
		t.Fatal(err)
	}
	s, err := m.AlterSources(ctx, before.ID, []string{jsonURL, rawURL, tuicURL}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Sources) != 3 || len(s.Configs) != 2 {
		t.Fatalf("expected three sources and two unique nodes, got %d sources and %d nodes", len(s.Sources), len(s.Configs))
	}
	if s.Configs[0].ID != before.Configs[0].ID || s.Configs[0].Profile == nil || s.Configs[1].Protocol != "tuic" {
		t.Fatal("merge changed the pinned ID or lost a provider profile")
	}
	if _, err := m.Add(ctx, rawURL, "", 0); !errors.Is(err, ErrExists) {
		t.Fatalf("source assigned to more than one group: %v", err)
	}

	delete(bodies, jsonURL)
	bodies[rawURL] = vlessRaw + "\n" + trojanWS
	s, err = m.Update(ctx, before.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Configs) != 3 || len(s.Sources[0].Configs) != 1 || !s.Sources[0].Failed || s.LastError == "" {
		t.Fatal("a failed source discarded its cached nodes or prevented another source from refreshing")
	}
	if strings.Contains(s.LastError, jsonURL) {
		t.Fatal("refresh error exposed the subscription URL")
	}

	s, err = m.AlterSources(ctx, before.ID, []string{tuicURL}, nil, nil)
	if err != nil || len(s.Sources) != 1 || len(s.Configs) != 1 || s.Configs[0].Protocol != "tuic" {
		t.Fatalf("removing sources did not preserve the remaining server: %v", err)
	}
}
