package mobile

import (
	"context"
	"errors"
	"net"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/Gozargah/Masir/libs/masir-daemon/internal/xray"
	"github.com/xtls/xray-core/common/platform"
	"github.com/xtls/xray-core/transport/internet"
)

// SocketProtector lets VpnService keep only Masir's own outbound sockets out of its TUN.
type SocketProtector interface {
	ProtectSocket(fd int32) bool
}

type protectedSockets struct{ protector SocketProtector }

var (
	currentProtector atomic.Pointer[protectedSockets]
	registerOnce     sync.Once
	controllerErr    error
	engineMu         sync.Mutex
	engine           *xray.Core
)

func protectSocket(_ string, _ string, conn syscall.RawConn) error {
	active := currentProtector.Load()
	if active == nil {
		return nil
	}
	var ok bool
	if err := conn.Control(func(fd uintptr) { ok = active.protector.ProtectSocket(int32(fd)) }); err != nil {
		return err
	}
	if !ok {
		return errors.New("Android refused to protect the VPN socket")
	}
	return nil
}

func subscriptionDial(ctx context.Context, network, address string) (net.Conn, error) {
	dialer := net.Dialer{Timeout: 12 * time.Second, Control: protectSocket}
	return dialer.DialContext(ctx, network, address)
}

// Start connects the engine to an open descriptor owned by Android VpnService.
func Start(fd int32, profile, directDomains, assetDir string, strict bool, protector SocketProtector) error {
	engineMu.Lock()
	defer engineMu.Unlock()
	if fd < 3 || protector == nil {
		return errors.New("Android VPN interface is unavailable")
	}
	if engine != nil {
		return errors.New("the VPN is already running")
	}
	p, err := connectionProfile(profile, directDomains, strict)
	if err != nil {
		return err
	}
	registerOnce.Do(func() {
		controllerErr = internet.RegisterDialerController(protectSocket)
	})
	if controllerErr != nil {
		return controllerErr
	}
	if err := os.Setenv(platform.TunFdKey, strconv.Itoa(int(fd))); err != nil {
		return err
	}
	defer os.Unsetenv(platform.TunFdKey)
	currentProtector.Store(&protectedSockets{protector: protector})
	c := xray.New(assetDir)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := c.Apply(ctx, p); err != nil {
		c.Close()
		currentProtector.Store(nil)
		return err
	}
	engine = c
	return nil
}

func Stop() {
	engineMu.Lock()
	defer engineMu.Unlock()
	if engine != nil {
		engine.Close()
		engine = nil
	}
	currentProtector.Store(nil)
}
