package xray

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"sync"

	"github.com/Gozargah/Masir/libs/masir-daemon/internal/amneziawg"
	awgconn "github.com/amnezia-vpn/amneziawg-go/v3/conn"
	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/features/dns"
	"github.com/xtls/xray-core/features/stats"
	"github.com/xtls/xray-core/proxy/wireguard"
	"github.com/xtls/xray-core/transport/internet"
)

// Use Xray's system dialer so Masir's TUN and probe socket controllers also
// apply to the UDP packets carrying the AmneziaWG tunnel.
type amneziaBind struct {
	ctx        context.Context
	socket     *internet.SocketConfig
	endpoints  map[string]netip.AddrPort
	example    [2]netip.AddrPort
	up, down   stats.Counter
	mu         sync.Mutex
	packetConn [2]net.PacketConn
}

func newAmneziaBind(ctx context.Context, peers []amneziawg.Peer, resolver dns.Client, socket *internet.SocketConfig, up, down stats.Counter) (*amneziaBind, error) {
	b := &amneziaBind{
		ctx:       context.WithoutCancel(ctx),
		socket:    socket,
		endpoints: make(map[string]netip.AddrPort, len(peers)),
		up:        up,
		down:      down,
	}
	for _, peer := range peers {
		host, port, _ := net.SplitHostPort(peer.Endpoint) // validated by amneziaSettings
		num, _ := strconv.ParseUint(port, 10, 16)
		ip, err := netip.ParseAddr(host)
		if err != nil {
			ips, _, err := resolver.LookupIP(host, dns.IPOption{IPv4Enable: true, IPv6Enable: true})
			if err != nil {
				return nil, fmt.Errorf("resolve AmneziaWG endpoint %s: %w", host, err)
			}
			if len(ips) == 0 {
				return nil, fmt.Errorf("resolve AmneziaWG endpoint %s: %w", host, dns.ErrEmptyResponse)
			}
			ip, _ = netip.AddrFromSlice(ips[0])
		}
		ep := netip.AddrPortFrom(ip.Unmap(), uint16(num))
		b.endpoints[peer.Endpoint] = ep
		b.example[amneziaFamily(ep.Addr())] = ep
	}
	return b, nil
}

func amneziaFamily(ip netip.Addr) int {
	if ip.Is4() {
		return 0
	}
	return 1
}

func (b *amneziaBind) Open(_ uint16) ([]awgconn.ReceiveFunc, uint16, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.packetConn[0] != nil || b.packetConn[1] != nil {
		return nil, 0, awgconn.ErrBindAlreadyOpen
	}
	var receivers []awgconn.ReceiveFunc
	var localPort uint16
	for i, ep := range b.example {
		if !ep.IsValid() {
			continue
		}
		dest := xnet.UDPDestination(xnet.IPAddress(ep.Addr().AsSlice()), xnet.Port(ep.Port()))
		conn, err := internet.DialSystem(b.ctx, dest, b.socket)
		if err != nil {
			b.closeSockets()
			return nil, 0, err
		}
		if addr, ok := conn.LocalAddr().(*net.UDPAddr); ok {
			localPort = uint16(addr.Port)
		}
		var packets net.PacketConn
		if wrapped, ok := conn.(*xnet.PacketConnWrapper); ok {
			packets = wrapped.PacketConn
		} else {
			packets = &internet.FakePacketConn{Conn: conn}
		}
		socket := &wireguard.PacketCounterConnection{PacketConn: packets, ReadCounter: b.down, WriteCounter: b.up}
		b.packetConn[i] = socket
		receivers = append(receivers, func(bufs [][]byte, sizes []int, eps []awgconn.Endpoint) (int, error) {
			size, from, err := socket.ReadFrom(bufs[0])
			if err != nil {
				return 0, err
			}
			addr, ok := from.(*net.UDPAddr)
			if !ok {
				return 0, errors.New("AmneziaWG received a packet without a UDP source")
			}
			source := addr.AddrPort()
			sizes[0] = size
			eps[0] = &awgconn.StdNetEndpoint{AddrPort: netip.AddrPortFrom(source.Addr().Unmap(), source.Port())}
			return 1, nil
		})
	}
	if len(receivers) == 0 {
		return nil, 0, errors.New("AmneziaWG has no usable endpoints")
	}
	return receivers, localPort, nil
}

func (b *amneziaBind) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.closeSockets()
}

func (b *amneziaBind) closeSockets() error {
	var err error
	for i, socket := range b.packetConn {
		if socket != nil {
			err = errors.Join(err, socket.Close())
			b.packetConn[i] = nil
		}
	}
	return err
}

func (b *amneziaBind) Send(bufs [][]byte, endpoint awgconn.Endpoint) error {
	ep, ok := endpoint.(*awgconn.StdNetEndpoint)
	if !ok {
		return awgconn.ErrWrongEndpointType
	}
	b.mu.Lock()
	socket := b.packetConn[amneziaFamily(ep.Addr())]
	b.mu.Unlock()
	if socket == nil {
		return net.ErrClosed
	}
	addr := net.UDPAddrFromAddrPort(ep.AddrPort)
	for _, payload := range bufs {
		if _, err := socket.WriteTo(payload, addr); err != nil {
			return err
		}
	}
	return nil
}

func (b *amneziaBind) ParseEndpoint(value string) (awgconn.Endpoint, error) {
	ep, ok := b.endpoints[value]
	if !ok || !ep.IsValid() {
		return nil, errors.New("AmneziaWG endpoint was not resolved")
	}
	return &awgconn.StdNetEndpoint{AddrPort: ep}, nil
}

// The socket controller binds the physical interface independently of a mark.
func (*amneziaBind) SetMark(uint32) error { return nil }
func (*amneziaBind) BatchSize() int       { return 1 }
