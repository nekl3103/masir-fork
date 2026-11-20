package xray

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/Gozargah/Masir/libs/masir-daemon/internal/amneziawg"
	awgdevice "github.com/amnezia-vpn/amneziawg-go/v3/device"
	"github.com/amnezia-vpn/amneziawg-go/v3/tun/netstack"
	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/core"
	_ "github.com/xtls/xray-core/main/distro/all"
	"github.com/xtls/xray-core/transport/internet"
)

func TestAmneziaWGAndDirectRoutes(t *testing.T) {
	clientKey, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serverKey, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	const serverEndpoint = "127.0.0.1:51820"
	const clientEndpoint = "127.0.0.1:51821"
	const directTarget = "127.0.0.1:8888"
	serverSocket := newTestPacketSocket(serverEndpoint)
	clientSocket := newTestPacketSocket(clientEndpoint)
	serverSocket.peer, clientSocket.peer = clientSocket, serverSocket
	internet.UseAlternativeSystemDialer(internet.WithAdapter(amneziaTestDialer{
		udp:          map[string]*testPacketSocket{serverEndpoint: clientSocket, clientEndpoint: serverSocket},
		directTarget: directTarget,
	}))
	defer internet.UseAlternativeSystemDialer(nil)

	serverIP := netip.MustParseAddr("10.66.0.1")
	tun, stack, err := netstack.CreateNetTUN([]netip.Addr{serverIP}, nil, 1420)
	if err != nil {
		t.Fatal(err)
	}
	bind, err := newAmneziaBind(context.Background(), []amneziawg.Peer{{Endpoint: clientEndpoint}}, nil, nil, nil, nil)
	if err != nil {
		tun.Close()
		t.Fatal(err)
	}
	server := awgdevice.NewDevice(tun, bind, awgdevice.NewLogger(awgdevice.LogLevelSilent, ""))
	defer server.Close()
	settings := fmt.Sprintf("private_key=%s\ns1=24\ns2=24\ns3=24\ns4=24\nh1=100-199\nh2=200-299\nh3=300-399\nh4=400-499\npublic_key=%s\nendpoint=%s\nallowed_ip=10.66.0.2/32\n",
		hex.EncodeToString(serverKey.Bytes()), hex.EncodeToString(clientKey.PublicKey().Bytes()), clientEndpoint)
	if err := server.IpcSet(settings); err != nil {
		t.Fatal(err)
	}
	if err := server.Up(); err != nil {
		t.Fatal(err)
	}
	client := amneziawg.Config{
		SecretKey: base64.StdEncoding.EncodeToString(clientKey.Bytes()),
		Address:   []string{"10.66.0.2/32"},
		Options: map[string]string{
			"s1": "24", "s2": "24", "s3": "24", "s4": "24",
			"h1": "100-199", "h2": "200-299", "h3": "300-399", "h4": "400-499",
		},
		Peers: []amneziawg.Peer{{
			PublicKey:  base64.StdEncoding.EncodeToString(serverKey.PublicKey().Bytes()),
			Endpoint:   serverEndpoint,
			AllowedIPs: []string{"10.66.0.0/24"},
		}},
	}
	clientJSON, err := json.Marshal(client)
	if err != nil {
		t.Fatal(err)
	}
	raw := fmt.Appendf(nil, `{
		"log":{"loglevel":"none"},
		"outbounds":[
			{"protocol":"amneziawg","tag":"vpn","settings":%s},
			{"protocol":"freedom","tag":"direct"}
		],
		"routing":{"rules":[{"type":"field","ip":["127.0.0.0/8"],"outboundTag":"direct"}]}
	}`, clientJSON)
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

	tunneled, err := stack.ListenTCPAddrPort(netip.AddrPortFrom(serverIP, 0))
	if err != nil {
		t.Fatal(err)
	}
	defer tunneled.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go func() {
		conn, err := tunneled.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = io.WriteString(conn, "amneziawg")
	}()
	for _, sample := range []struct {
		address string
		reply   string
	}{{directTarget, "direct"}, {tunneled.Addr().String(), "amneziawg"}} {
		destination, err := xnet.ParseDestination("tcp:" + sample.address)
		if err != nil {
			t.Fatal(err)
		}
		conn, err := core.Dial(ctx, instance, destination)
		if err != nil {
			t.Fatal(err)
		}
		checkAmneziaReply(t, ctx, conn, sample.reply)
	}

	udp, err := stack.ListenUDPAddrPort(netip.AddrPortFrom(serverIP, 0))
	if err != nil {
		t.Fatal(err)
	}
	defer udp.Close()
	go func() {
		packet := make([]byte, 32)
		n, from, err := udp.ReadFrom(packet)
		if err == nil && string(packet[:n]) == "ping" {
			_, _ = udp.WriteTo([]byte("pong"), from)
		}
	}()
	destination, err := xnet.ParseDestination("udp:" + udp.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	conn, err := core.Dial(ctx, instance, destination)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(conn, "ping"); err != nil {
		conn.Close()
		t.Fatal(err)
	}
	checkAmneziaReply(t, ctx, conn, "pong")
}

func checkAmneziaReply(t *testing.T, ctx context.Context, conn net.Conn, expected string) {
	t.Helper()
	defer conn.Close()
	reply := make([]byte, len(expected))
	read := make(chan error, 1)
	go func() {
		_, err := io.ReadFull(conn, reply)
		read <- err
	}()
	select {
	case err := <-read:
		if err != nil || string(reply) != expected {
			t.Fatalf("%s response: %q, %v", expected, reply, err)
		}
	case <-ctx.Done():
		t.Fatalf("%s response: %v", expected, ctx.Err())
	}
}

type amneziaTestDialer struct {
	udp          map[string]*testPacketSocket
	directTarget string
}

func (d amneziaTestDialer) Dial(network, addr string) (net.Conn, error) {
	if socket := d.udp[addr]; network == "udp" && socket != nil {
		return &xnet.PacketConnWrapper{PacketConn: socket, Dest: socket.peer.addr}, nil
	}
	if network == "tcp" && addr == d.directTarget {
		client, server := net.Pipe()
		go func() {
			defer server.Close()
			_, _ = io.WriteString(server, "direct")
		}()
		return client, nil
	}
	return nil, fmt.Errorf("unexpected test dial %s %s", network, addr)
}

// Move encrypted datagrams between Xray and the peer without host UDP sockets.
type testPacketSocket struct {
	net.PacketConn
	addr *net.UDPAddr
	peer *testPacketSocket
	in   chan []byte
	done chan struct{}
	once sync.Once
}

func newTestPacketSocket(addr string) *testPacketSocket {
	return &testPacketSocket{
		addr: net.UDPAddrFromAddrPort(netip.MustParseAddrPort(addr)),
		in:   make(chan []byte, 64),
		done: make(chan struct{}),
	}
}

func (s *testPacketSocket) ReadFrom(p []byte) (int, net.Addr, error) {
	select {
	case <-s.done:
		return 0, nil, net.ErrClosed
	case packet := <-s.in:
		return copy(p, packet), s.peer.addr, nil
	}
}

func (s *testPacketSocket) WriteTo(p []byte, addr net.Addr) (int, error) {
	if addr.String() != s.peer.addr.String() {
		return 0, fmt.Errorf("unexpected test packet destination %s", addr)
	}
	select {
	case <-s.done:
		return 0, net.ErrClosed
	case <-s.peer.done:
		return 0, net.ErrClosed
	case s.peer.in <- append([]byte(nil), p...):
		return len(p), nil
	}
}

func (s *testPacketSocket) LocalAddr() net.Addr { return s.addr }

func (s *testPacketSocket) Close() error {
	s.once.Do(func() { close(s.done) })
	return nil
}
