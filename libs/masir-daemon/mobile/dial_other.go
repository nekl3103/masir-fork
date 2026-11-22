//go:build !android

package mobile

import (
	"context"
	"net"
	"time"
)

func subscriptionDial(ctx context.Context, network, address string) (net.Conn, error) {
	dialer := net.Dialer{Timeout: 12 * time.Second}
	return dialer.DialContext(ctx, network, address)
}
