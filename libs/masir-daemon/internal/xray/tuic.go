package xray

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"net"
	"os"
	"sync"
	"time"

	"github.com/Gozargah/Masir/libs/masir-daemon/internal/tuic"
	"github.com/gofrs/uuid/v5"
	singtuic "github.com/sagernet/sing-quic/tuic"
	M "github.com/sagernet/sing/common/metadata"
	STLS "github.com/sagernet/sing/common/tls"
	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/buf"
	xnet "github.com/xtls/xray-core/common/net"
	xserial "github.com/xtls/xray-core/common/serial"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/features/dns"
	"github.com/xtls/xray-core/features/policy"
	"github.com/xtls/xray-core/infra/conf"
	"github.com/xtls/xray-core/transport"
	"github.com/xtls/xray-core/transport/internet"
	"google.golang.org/protobuf/types/known/anypb"
)

const tuicConfigType = "type.masir.local/outbound.tuic"

var registerTUIC = sync.OnceFunc(func() {
	common.Must(common.RegisterConfig((*anypb.Any)(nil), func(ctx context.Context, raw any) (any, error) {
		message := raw.(*anypb.Any)
		if message.TypeUrl != tuicConfigType {
			return nil, errors.New("unknown Masir outbound settings")
		}
		var cfg tuic.Config
		if json.Unmarshal(message.Value, &cfg) != nil {
			return nil, errors.New("invalid TUIC settings")
		}
		if err := cfg.Validate(); err != nil {
			return nil, err
		}
		id, _ := uuid.FromString(cfg.UUID)
		serverName := cfg.ServerName
		if serverName == "" {
			serverName = cfg.Address
		}
		if len(cfg.ALPN) == 0 {
			cfg.ALPN = []string{"h3"}
		}
		instance := core.MustFromContext(ctx)
		resolver := instance.GetFeature(dns.ClientType()).(dns.Client)
		stream, _ := session.StreamSettingsFromContext(ctx).(*internet.MemoryStreamConfig)
		dialer := tuicSocketDialer{resolver: resolver}
		if stream != nil {
			dialer.socket = stream.SocketSettings
		}
		tlsConfig := &tuicTLS{config: &tls.Config{
			ServerName: serverName, NextProtos: cfg.ALPN, MinVersion: tls.VersionTLS13,
			InsecureSkipVerify: cfg.SkipCertVerify,
		}}
		lifetime, cancel := context.WithCancel(context.WithoutCancel(ctx))
		client, err := singtuic.NewClient(singtuic.ClientOptions{
			Context: lifetime, Dialer: dialer, ServerAddress: M.ParseSocksaddr(cfg.Endpoint()),
			TLSConfig: tlsConfig, UUID: id, Password: cfg.Password,
			CongestionControl: cfg.CongestionControl, UDPStream: cfg.UDPRelayMode == "quic",
			ZeroRTTHandshake: cfg.ZeroRTTHandshake,
		})
		if err != nil {
			cancel()
			return nil, err
		}
		return &tuicOutbound{client: client, cancel: cancel, policy: instance.GetFeature(policy.ManagerType()).(policy.Manager)}, nil
	}))
})

func tuicSettings(outbound conf.OutboundDetourConfig) (*xserial.TypedMessage, error) {
	var cfg tuic.Config
	if outbound.Settings == nil || json.Unmarshal(*outbound.Settings, &cfg) != nil {
		return nil, errors.New("invalid TUIC settings")
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	canonical, _ := json.Marshal(cfg)
	registerTUIC()
	return xserial.ToTypedMessage(&anypb.Any{TypeUrl: tuicConfigType, Value: canonical}), nil
}

type tuicTLS struct {
	config  *tls.Config
	timeout time.Duration
}

func (t *tuicTLS) ServerName() string                   { return t.config.ServerName }
func (t *tuicTLS) SetServerName(name string)            { t.config.ServerName = name }
func (t *tuicTLS) NextProtos() []string                 { return t.config.NextProtos }
func (t *tuicTLS) SetNextProtos(p []string)             { t.config.NextProtos = p }
func (t *tuicTLS) HandshakeTimeout() time.Duration      { return t.timeout }
func (t *tuicTLS) SetHandshakeTimeout(d time.Duration)  { t.timeout = d }
func (t *tuicTLS) STDConfig() (*tls.Config, error)      { return t.config.Clone(), nil }
func (t *tuicTLS) Clone() STLS.Config                   { return &tuicTLS{config: t.config.Clone(), timeout: t.timeout} }
func (t *tuicTLS) Client(c net.Conn) (STLS.Conn, error) { return tls.Client(c, t.config), nil }

type tuicSocketDialer struct {
	resolver dns.Client
	socket   *internet.SocketConfig
}

func (d tuicSocketDialer) DialContext(ctx context.Context, network string, dst M.Socksaddr) (net.Conn, error) {
	target, err := xnet.ParseDestination(network + ":" + dst.String())
	if err != nil {
		return nil, err
	}
	if target.Address.Family().IsDomain() {
		ips, _, err := d.resolver.LookupIP(target.Address.Domain(), dns.IPOption{IPv4Enable: true, IPv6Enable: true})
		if err != nil {
			return nil, err
		}
		if len(ips) == 0 {
			return nil, dns.ErrEmptyResponse
		}
		target.Address = xnet.IPAddress(ips[0])
	}
	// Use the same socket controllers as other Xray outbounds when TUN is active.
	return internet.DialSystem(ctx, target, d.socket)
}

func (d tuicSocketDialer) ListenPacket(ctx context.Context, dst M.Socksaddr) (net.PacketConn, error) {
	c, err := d.DialContext(ctx, "udp", dst)
	if err != nil {
		return nil, err
	}
	if p, ok := c.(*xnet.PacketConnWrapper); ok {
		return p.PacketConn, nil
	}
	return &internet.FakePacketConn{Conn: c}, nil
}

type tuicOutbound struct {
	client *singtuic.Client
	cancel context.CancelFunc
	policy policy.Manager
}

func (h *tuicOutbound) Process(ctx context.Context, link *transport.Link, dialer internet.Dialer) error {
	outs := session.OutboundsFromContext(ctx)
	if len(outs) == 0 || !outs[len(outs)-1].Target.IsValid() {
		return errors.New("TUIC target is missing")
	}
	out := outs[len(outs)-1]
	out.Name = tuic.Protocol
	dialer.SetOutboundGateway(ctx, out)
	target := out.Target
	timeouts := h.policy.ForLevel(0).Timeouts
	dialCtx := ctx
	if timeouts.Handshake > 0 {
		var cancel context.CancelFunc
		dialCtx, cancel = context.WithTimeout(ctx, timeouts.Handshake)
		defer cancel()
	}
	switch target.Network {
	case xnet.Network_TCP:
		conn, err := h.client.DialConn(dialCtx, M.ParseSocksaddr(target.NetAddr()))
		if err != nil {
			return err
		}
		defer conn.Close()
		return relayOutbound(ctx, link, conn, buf.NewReader(conn), buf.NewWriter(conn), timeouts)
	case xnet.Network_UDP:
		conn, err := h.client.ListenPacket(dialCtx)
		if err != nil {
			return err
		}
		defer conn.Close()
		packets := tuicPacketLink{conn: conn, target: target}
		return relayOutbound(ctx, link, nil, packets, packets, timeouts)
	default:
		return errors.New("TUIC requires a TCP or UDP target")
	}
}

func (h *tuicOutbound) Close() error {
	h.cancel()
	return h.client.CloseWithError(os.ErrClosed)
}

type tuicPacketLink struct {
	conn   net.PacketConn
	target xnet.Destination
}

func (p tuicPacketLink) ReadMultiBuffer() (buf.MultiBuffer, error) {
	b := buf.NewWithSize(65535)
	b.Extend(65535)
	n, addr, err := p.conn.ReadFrom(b.Bytes())
	if err != nil {
		b.Release()
		return nil, err
	}
	dest, err := xnet.ParseDestination("udp:" + addr.String())
	if err != nil {
		b.Release()
		return nil, err
	}
	b.Resize(0, int32(n))
	b.UDP = &dest
	return buf.MultiBuffer{b}, nil
}

func (p tuicPacketLink) WriteMultiBuffer(buffers buf.MultiBuffer) error {
	for i, b := range buffers {
		dest := p.target
		if b.UDP != nil {
			dest = *b.UDP
		}
		if _, err := p.conn.WriteTo(b.Bytes(), M.ParseSocksaddr(dest.NetAddr())); err != nil {
			buf.ReleaseMulti(buffers[i:])
			return err
		}
		b.Release()
	}
	return nil
}
