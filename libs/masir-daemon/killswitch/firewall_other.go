//go:build !darwin && !windows && (!linux || android)

package killswitch

import "context"

type Firewall struct{}

func (*Firewall) Enable(context.Context, string) error { return nil }
func (*Firewall) Disable(context.Context) error        { return nil }

func (*Firewall) AllowTunnel(context.Context, string) error { return nil }

func Reset(context.Context) error { return nil }
