package xray

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"sync"

	"github.com/Gozargah/Masir/libs/masir-daemon/internal/amneziawg"
	"github.com/Gozargah/Masir/libs/masir-daemon/internal/tuic"
	awgdevice "github.com/amnezia-vpn/amneziawg-go/v3/device"
	"github.com/amnezia-vpn/amneziawg-go/v3/tun/netstack"
	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/buf"
	xnet "github.com/xtls/xray-core/common/net"
	xserial "github.com/xtls/xray-core/common/serial"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/common/signal"
	"github.com/xtls/xray-core/common/task"
	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/features/dns"
	"github.com/xtls/xray-core/features/policy"
	"github.com/xtls/xray-core/features/stats"
	"github.com/xtls/xray-core/infra/conf"
	confserial "github.com/xtls/xray-core/infra/conf/serial"
	"github.com/xtls/xray-core/proxy/wireguard"
	"github.com/xtls/xray-core/transport"
	"github.com/xtls/xray-core/transport/internet"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"
)

var registerAmnezia = sync.OnceFunc(func() {
	common.Must(common.RegisterConfig((*structpb.Struct)(nil), func(ctx context.Context, raw any) (any, error) {
		b, err := protojson.Marshal(raw.(*structpb.Struct))
		if err != nil {
			return nil, err
		}
		var cfg amneziawg.Config
		if err := json.Unmarshal(b, &cfg); err != nil {
			return nil, err
		}
		addresses, resolvers, err := cfg.Addresses()
		if err != nil {
			return nil, err
		}
		instance := core.MustFromContext(ctx)
		stream, _ := session.StreamSettingsFromContext(ctx).(*internet.MemoryStreamConfig)
		handler := &amneziaOutbound{
			addresses: addresses,
			resolvers: resolvers,
			cfg:       cfg,
			dns:       instance.GetFeature(dns.ClientType()).(dns.Client),
			policy:    instance.GetFeature(policy.ManagerType()).(policy.Manager),
			log:       awgdevice.NewLogger(awgdevice.LogLevelSilent, ""),
		}
		if stream != nil {
			handler.socket = stream.SocketSettings
		}
		if tag := session.FullHandlerFromContext(ctx).Tag(); tag != "" {
			manager := instance.GetFeature(stats.ManagerType()).(stats.Manager)
			system := handler.policy.ForSystem().Stats
			if system.OutboundUplink {
				handler.up, _ = manager.GetOrRegisterCounter("outbound>>>" + tag + ">>>traffic>>>uplink")
			}
			if system.OutboundDownlink {
				handler.down, _ = manager.GetOrRegisterCounter("outbound>>>" + tag + ">>>traffic>>>downlink")
			}
		}
		return handler, nil
	}))
})

func amneziaSettings(outbound conf.OutboundDetourConfig) (*xserial.TypedMessage, error) {
	if outbound.Settings == nil {
		return nil, errors.New("AmneziaWG settings are required")
	}
	raw := *outbound.Settings
	var cfg amneziawg.Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, errors.New("invalid AmneziaWG settings")
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	// Marshal the typed settings to omit unknown provider fields.
	canonical, _ := json.Marshal(cfg)
	message := &structpb.Struct{}
	if err := protojson.Unmarshal(canonical, message); err != nil {
		return nil, errors.New("invalid AmneziaWG settings")
	}
	registerAmnezia()
	return xserial.ToTypedMessage(message), nil
}

// Xray owns the sender settings, outbound ordering, tags, routing and lifecycle.
// Use its standard JSON builder for all of those and attach our protocol handlers.
func loadConfig(raw []byte) (*core.Config, error) {
	doc, err := confserial.DecodeJSONConfig(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	settings := make(map[int]*xserial.TypedMessage)
	for i := range doc.OutboundConfigs {
		o := &doc.OutboundConfigs[i]
		proxy, err := prepareCustomOutbound(o)
		if err != nil {
			return nil, err
		}
		if proxy != nil {
			settings[i] = proxy
		}
	}
	built, err := doc.Build()
	if err != nil {
		return nil, err
	}
	for i, proxy := range settings {
		built.Outbound[i].ProxySettings = proxy
	}
	return built, nil
}

func prepareCustomOutbound(o *conf.OutboundDetourConfig) (*xserial.TypedMessage, error) {
	var build func(conf.OutboundDetourConfig) (*xserial.TypedMessage, error)
	switch strings.ToLower(o.Protocol) {
	case amneziawg.Protocol:
		build = amneziaSettings
	case tuic.Protocol:
		build = tuicSettings
	case "masque":
		return nil, validateMasqueOutbound(o)
	default:
		return nil, nil
	}
	proxy, err := build(*o)
	if err != nil {
		return nil, err
	}
	o.Protocol, o.Settings = "freedom", nil
	return proxy, nil
}

type amneziaOutbound struct {
	cfg       amneziawg.Config
	addresses []netip.Addr
	resolvers []netip.Addr
	dns       dns.Client
	policy    policy.Manager
	socket    *internet.SocketConfig
	log       *awgdevice.Logger
	up, down  stats.Counter

	mu     sync.Mutex
	closed bool
	device *awgdevice.Device
	stack  *netstack.Net
}

func (h *amneziaOutbound) start(ctx context.Context) (*netstack.Net, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil, net.ErrClosed
	}
	if h.stack != nil {
		return h.stack, nil
	}
	cfg := h.cfg
	ipc, err := cfg.UAPI()
	if err != nil {
		return nil, err
	}
	bind, err := newAmneziaBind(ctx, cfg.Peers, h.dns, h.socket, h.up, h.down)
	if err != nil {
		return nil, err
	}

	mtu := cfg.MTU
	if mtu == 0 {
		mtu = 1420
	}
	if mtu > awgdevice.MaxContentSize {
		return nil, errors.New("AmneziaWG MTU exceeds the platform maximum")
	}
	tun, stack, err := netstack.CreateNetTUN(h.addresses, h.resolvers, mtu)
	if err != nil {
		return nil, fmt.Errorf("create AmneziaWG network stack: %w", err)
	}
	device := awgdevice.NewDevice(tun, bind, h.log)
	if err := device.IpcSet(ipc); err != nil {
		device.Close()
		// Some upstream errors contain the supplied setting value.
		return nil, errors.New("AmneziaWG rejected the tunnel settings")
	}
	if err := device.Up(); err != nil {
		device.Close()
		return nil, fmt.Errorf("start AmneziaWG tunnel: %w", err)
	}
	h.device, h.stack = device, stack
	return stack, nil
}

func (h *amneziaOutbound) Process(ctx context.Context, link *transport.Link, dialer internet.Dialer) error {
	outbounds := session.OutboundsFromContext(ctx)
	if len(outbounds) == 0 {
		return errors.New("AmneziaWG target is missing")
	}
	ob := outbounds[len(outbounds)-1]
	target := ob.Target
	if !target.IsValid() || (target.Network != xnet.Network_TCP && target.Network != xnet.Network_UDP) {
		return errors.New("AmneziaWG requires a TCP or UDP target")
	}
	ob.Name = amneziawg.Protocol
	dialer.SetOutboundGateway(ctx, ob)
	if session.TimeoutOnlyFromContext(ctx) {
		ctx = context.WithoutCancel(ctx)
	}
	timeouts := h.policy.ForLevel(0).Timeouts

	stack, err := h.start(ctx)
	if err != nil {
		return err
	}
	// With no private resolver in the profile, honor the active Xray DNS policy.
	if target.Address.Family().IsDomain() && len(h.resolvers) == 0 {
		var options dns.IPOption
		for _, ip := range h.addresses {
			options.IPv4Enable = options.IPv4Enable || ip.Is4()
			options.IPv6Enable = options.IPv6Enable || ip.Is6()
		}
		ips, _, err := h.dns.LookupIP(target.Address.Domain(), options)
		if err != nil {
			return err
		}
		if len(ips) == 0 {
			return dns.ErrEmptyResponse
		}
		target.Address = xnet.IPAddress(ips[0])
	}
	dialCtx, done := ctx, func() {}
	if timeouts.Handshake > 0 {
		dialCtx, done = context.WithTimeout(ctx, timeouts.Handshake)
	}
	conn, err := stack.DialContext(dialCtx, target.Network.SystemString(), target.NetAddr())
	done()
	if err != nil {
		return err
	}
	defer conn.Close()

	reader, writer := buf.NewReader(conn), buf.NewWriter(conn)
	if target.Network == xnet.Network_UDP {
		udp := &wireguard.UDPConnClient{PacketConn: conn.(net.PacketConn), Dest: conn.RemoteAddr().(*net.UDPAddr)}
		reader, writer = udp, udp
	}
	return relayOutbound(ctx, link, conn, reader, writer, timeouts)
}

func relayOutbound(ctx context.Context, link *transport.Link, conn net.Conn, reader buf.Reader, writer buf.Writer, timeouts policy.Timeout) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	timer := signal.CancelAfterInactivity(ctx, cancel, timeouts.ConnectionIdle)
	request := func() error {
		defer timer.SetTimeout(timeouts.DownlinkOnly)
		if err := buf.Copy(link.Reader, writer, buf.UpdateActivity(timer)); err != nil {
			return err
		}
		if tcp, ok := conn.(interface{ CloseWrite() error }); ok {
			return tcp.CloseWrite()
		}
		return nil
	}
	response := func() error {
		defer timer.SetTimeout(timeouts.UplinkOnly)
		return buf.Copy(reader, link.Writer, buf.UpdateActivity(timer))
	}
	if err := task.Run(ctx, request, task.OnSuccess(response, task.Close(link.Writer))); err != nil {
		common.Interrupt(link.Reader)
		common.Interrupt(link.Writer)
		return err
	}
	return nil
}

func (h *amneziaOutbound) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	if h.device != nil {
		h.device.Close()
		h.device, h.stack = nil, nil
	}
	return nil
}
