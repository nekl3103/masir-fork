//go:build !darwin

package killswitch

import "context"

type Firewall struct{}

func (*Firewall) Enable(context.Context, string) error { return nil }
func (*Firewall) Disable(context.Context) error        { return nil }
