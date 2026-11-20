package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/Gozargah/Masir/libs/masir-daemon/internal/compose"
	"github.com/Gozargah/Masir/libs/masir-daemon/internal/store"
	"github.com/Gozargah/Masir/libs/masir-daemon/internal/xray"
	pb "github.com/Gozargah/Masir/libs/masir-proto/gen/masir/daemon/v1"
	"github.com/xtls/xray-core/common/session"
	"golang.org/x/net/idna"
)

func routeCheckDomain(raw string) (string, error) {
	domain, err := idna.Lookup.ToASCII(strings.TrimSuffix(strings.ToLower(strings.TrimSpace(raw)), "."))
	if err != nil || !strings.Contains(domain, ".") || len(domain) > 253 || net.ParseIP(domain) != nil ||
		strings.HasSuffix(domain, ".localhost") || strings.HasSuffix(domain, ".local") {
		return "", fmt.Errorf("enter a public hostname such as example.com: %w", xray.ErrInvalid)
	}
	for label := range strings.SplitSeq(domain, ".") {
		if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") ||
			strings.ContainsFunc(label, func(r rune) bool {
				return (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-'
			}) {
			return "", fmt.Errorf("enter a public hostname such as example.com: %w", xray.ErrInvalid)
		}
	}
	return domain, nil
}

func (s *Server) checkDomainRoute(ctx context.Context, rawDomain string) (*pb.TestRoutingResponse, error) {
	host, err := routeCheckDomain(rawDomain)
	if err != nil {
		return nil, toStatus(err)
	}
	s.mu.Lock()
	inbound := compose.ProxyInboundTag
	if s.running.tun {
		inbound = compose.TunInboundTag
	}
	s.mu.Unlock()
	res, err := s.xray.TestRoute(ctx, xray.RouteQuery{Domain: host, Port: 443, InboundTag: inbound})
	if err != nil {
		return nil, toStatus(err)
	}
	out := &pb.TestRoutingResponse{
		OutboundTag: res.OutboundTag, RuleTag: res.RuleTag, Matched: res.Matched,
		OutboundName: s.routeOutboundName(res),
	}
	if res.OutboundTag == "block" || res.OutboundTag == "" {
		return out, nil
	}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			ctx = session.SetForcedOutboundTagToContext(ctx, res.OutboundTag)
			return s.xray.Dial(ctx, network, address)
		},
		DisableKeepAlives: true,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport: transport, Timeout: 10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}

	var wg sync.WaitGroup
	wg.Go(func() {
		response, err := requestRouteProbe(ctx, client, http.MethodHead, "https://"+host)
		if err != nil {
			out.HttpError = err.Error()
			return
		}
		response.Body.Close()
		out.HttpStatus = uint32(response.StatusCode)
	})
	wg.Go(func() {
		response, err := requestRouteProbe(ctx, client, http.MethodGet, "https://api64.ipify.org")
		if err != nil {
			out.EgressError = err.Error()
			return
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			out.EgressError = response.Status
			return
		}
		body, err := io.ReadAll(io.LimitReader(response.Body, 128))
		addr, parseErr := netip.ParseAddr(strings.TrimSpace(string(body)))
		if err != nil || parseErr != nil || addr.Zone() != "" {
			out.EgressError = "IP check returned no valid address"
		} else {
			out.EgressIp = addr.String()
		}
	})
	wg.Wait()
	return out, nil
}

func requestRouteProbe(ctx context.Context, client *http.Client, method, endpoint string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, errors.New("connection timed out")
		}
		return nil, errors.New("HTTPS connection failed")
	}
	return resp, nil
}

func (s *Server) routeOutboundName(res xray.RouteResult) string {
	switch res.OutboundTag {
	case "direct":
		return "Direct"
	case "block":
		return "Blocked"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, resolved := range s.resolved {
		target := resolved.Rule.Target
		if resolved.Rule.Tag == res.RuleTag && !resolved.FellBack && target.Kind == store.TargetConfig {
			if config, ok := s.subs.Find(target.SubscriptionID, target.ConfigID, target.Name); ok {
				return config.Name
			}
		}
	}
	if res.OutboundTag == "proxy" || strings.HasPrefix(res.OutboundTag, "masir-profile/") {
		if active, err := s.subs.Active(); err == nil && active.Name != "" {
			return active.Name
		}
	}
	return res.OutboundTag
}
