package xray

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strings"

	"github.com/xtls/xray-core/app/router"
	"github.com/xtls/xray-core/common"
	corenet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/core"
	routingsession "github.com/xtls/xray-core/features/routing/session"
	"github.com/xtls/xray-core/infra/conf"
)

var (
	ErrNotRunning = errors.New("xray is not running")
	ErrInvalid    = errors.New("invalid argument")
)

type RouteQuery struct {
	Domain     string
	IP         string
	Port       uint32
	Network    string // "tcp" (default) or "udp"
	InboundTag string
}

type RouteResult struct {
	OutboundTag string
	Matched     bool // false means no rule matched and Xray falls back to the first outbound
}

func (c *Core) TestRoute(ctx context.Context, q RouteQuery) (RouteResult, error) {
	rc := &routeContext{
		InboundTag:   q.InboundTag,
		TargetDomain: q.Domain,
		TargetPort:   q.Port,
	}
	switch strings.ToLower(q.Network) {
	case "", "tcp":
		rc.Network = corenet.Network_TCP
	case "udp":
		rc.Network = corenet.Network_UDP
	default:
		return RouteResult{}, fmt.Errorf("network %q: %w", q.Network, ErrInvalid)
	}
	if q.IP != "" {
		ip := net.ParseIP(q.IP)
		if ip == nil {
			return RouteResult{}, fmt.Errorf("ip %q: %w", q.IP, ErrInvalid)
		}
		if v4 := ip.To4(); v4 != nil {
			ip = v4 // Xray's IP matchers treat a 16-byte address as IPv6
		}
		rc.TargetIPs = []corenet.IP{ip}
	}
	if q.Domain == "" && q.IP == "" {
		return RouteResult{}, fmt.Errorf("domain or ip is required: %w", ErrInvalid)
	}

	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.routing == nil {
		return RouteResult{}, ErrNotRunning
	}
	if err := ctx.Err(); err != nil {
		return RouteResult{}, err
	}
	res, err := c.routing.PickRoute(rc)
	if err != nil {
		if errors.Is(err, common.ErrNoClue) {
			return RouteResult{OutboundTag: firstTag(c.cur.outbounds)}, nil
		}
		return RouteResult{}, err
	}
	return RouteResult{OutboundTag: res.GetOutboundTag(), Matched: true}, nil
}

// ValidateOutbound reports whether Xray can build raw as an outbound.
func ValidateOutbound(raw json.RawMessage) error {
	_, err := buildOutbound(raw)
	return err
}

// ValidateInbound reports whether Xray can build raw as an inbound.
func ValidateInbound(raw json.RawMessage) error {
	_, err := buildInbound(raw)
	return err
}

// ValidateRule reports whether Xray can build raw as a routing rule.
func ValidateRule(raw json.RawMessage) error {
	cfg, err := buildRule(raw)
	if err != nil {
		return err
	}
	if err := validateRuleAttributes(cfg); err != nil {
		return err
	}
	_, err = cfg.Rule[0].BuildCondition()
	return err
}

// Xray's condition builder assumes attribute expressions are valid.
func validateRuleAttributes(cfg *router.Config) error {
	for i, rule := range cfg.Rule {
		for _, pattern := range rule.Attributes {
			if _, err := regexp.Compile(pattern); err != nil {
				return fmt.Errorf("routing rule %d has an invalid attribute expression", i+1)
			}
		}
	}
	return nil
}

func buildInbound(raw json.RawMessage) (*core.InboundHandlerConfig, error) {
	var ic conf.InboundDetourConfig
	if err := json.Unmarshal(raw, &ic); err != nil {
		return nil, err
	}
	return ic.Build()
}

func buildOutbound(raw json.RawMessage) (*core.OutboundHandlerConfig, error) {
	var oc conf.OutboundDetourConfig
	if err := json.Unmarshal(raw, &oc); err != nil {
		return nil, err
	}
	settings, err := prepareCustomOutbound(&oc)
	if err != nil {
		return nil, err
	}
	outbound, err := oc.Build()
	if err != nil {
		return nil, err
	}
	if settings != nil {
		outbound.ProxySettings = settings
	}
	return outbound, nil
}

// Check transport requirements during subscription import. Xray's MASQUE
// client otherwise reports an incompatible transport only when it starts.
func validateMasqueOutbound(outbound *conf.OutboundDetourConfig) error {
	stream := outbound.StreamSetting
	if stream == nil || !strings.EqualFold(stream.Security, "tls") {
		return errors.New("MASQUE requires TLS")
	}
	network := stream.Network
	if stream.Method != nil {
		network = stream.Method
	}
	if network == nil {
		return errors.New("MASQUE requires the masque transport")
	}
	transport, err := network.Build()
	if err != nil || transport != "masque" {
		return errors.New("MASQUE requires the masque transport")
	}
	if mux := outbound.MuxSettings; mux != nil && mux.Enabled {
		return errors.New("MASQUE does not support Xray Mux")
	}
	return nil
}

func buildRule(raw json.RawMessage) (*router.Config, error) {
	rc := conf.RouterConfig{RuleList: []json.RawMessage{raw}}
	return rc.Build() // resolves geosite/geoip here, so the daemon needs the .dat files
}

// routeContext supplies query fields without depending on RPC configuration messages.
// Empty session fields preserve Xray's defaults for the remaining routing inputs.
type routeContext struct {
	routingsession.Context
	InboundTag   string
	TargetDomain string
	TargetPort   uint32
	TargetIPs    []corenet.IP
	Network      corenet.Network
}

func (c *routeContext) GetInboundTag() string       { return c.InboundTag }
func (c *routeContext) GetTargetDomain() string     { return c.TargetDomain }
func (c *routeContext) GetTargetPort() corenet.Port { return corenet.Port(c.TargetPort) }
func (c *routeContext) GetTargetIPs() []corenet.IP  { return c.TargetIPs }
func (c *routeContext) GetNetwork() corenet.Network { return c.Network }
