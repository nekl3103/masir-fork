package killswitch

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"time"

	"github.com/tailscale/wf"
	"golang.org/x/sys/windows"
	"golang.zx2c4.com/wireguard/windows/tunnel/winipcfg"
)

var windowsLayers = [...]wf.LayerID{wf.LayerALEAuthConnectV4, wf.LayerALEAuthConnectV6}

func filterID(slot uint8) windows.GUID {
	return windows.GUID{Data1: 0x89632df5, Data2: 0x4ba1, Data3: 0x4039, Data4: [8]byte{0x9c, 0x8b, 0x73, 0x61, 0x61, 0xd6, 0xa9, slot}}
}

var masirSublayer = wf.SublayerID(filterID(0))

type Firewall struct {
	armed, disabled bool
	tunnel          *wf.Session
	tunnelLUID      winipcfg.LUID
}

// Enable installs the default block and its limited exemptions before Xray starts.
// The rules persist across daemon restarts. The tunnel exemption is short lived.
func (f *Firewall) Enable(ctx context.Context, _ string) error {
	if f.armed {
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	app, err := wf.AppID(exe)
	if err != nil {
		return err
	}
	session, err := openWFP(false)
	if err != nil {
		return err
	}
	defer session.Close()
	sublayer := &wf.Sublayer{ID: masirSublayer, Name: "Masir Kill Switch", Weight: 0xffff, Persistent: true}
	if err := session.AddSublayer(sublayer); err != nil && !wfpHas(err, windows.FWP_E_ALREADY_EXISTS) {
		return err
	}
	f.disabled = false // Disable must clean up even after a partial installation.
	for _, rule := range persistentWindowsRules(app) {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := session.AddRule(rule); err != nil && !wfpHas(err, windows.FWP_E_ALREADY_EXISTS) {
			return fmt.Errorf("%s: %w", rule.Name, err)
		}
	}
	f.armed = true
	return nil
}

func (f *Firewall) AllowTunnel(ctx context.Context, name string) error {
	iface, err := net.InterfaceByName(name)
	if err != nil {
		return err
	}
	luid, err := winipcfg.LUIDFromIndex(uint32(iface.Index))
	if err != nil {
		return err
	}
	if f.tunnel != nil && f.tunnelLUID == luid {
		return nil
	}
	if err := f.closeTunnel(); err != nil {
		return err
	}
	session, err := openWFP(true)
	if err != nil {
		return err
	}
	for i, layer := range windowsLayers {
		if err := ctx.Err(); err != nil {
			session.Close()
			return err
		}
		rule := &wf.Rule{
			ID: wf.RuleID(filterID(uint8(20 + i))), Layer: layer, Sublayer: masirSublayer,
			Name: "Masir VPN interface", Weight: 30, Action: wf.ActionPermit,
			Conditions: []*wf.Match{
				equal(wf.FieldIPLocalInterface, uint64(luid)),
				equal(wf.FieldIPNexthopInterface, uint64(luid)),
			},
		}
		if err := session.AddRule(rule); err != nil {
			session.Close()
			return err
		}
	}
	f.tunnel, f.tunnelLUID = session, luid
	return nil
}

func (f *Firewall) Disable(ctx context.Context) error {
	if f.disabled {
		return nil
	}
	if err := f.closeTunnel(); err != nil {
		return err
	}
	if err := Reset(ctx); err != nil {
		return err
	}
	f.armed, f.disabled = false, true
	return nil
}

func (f *Firewall) closeTunnel() error {
	if f.tunnel == nil {
		return nil
	}
	if err := f.tunnel.Close(); err != nil {
		return err
	}
	f.tunnel, f.tunnelLUID = nil, 0
	return nil
}

func Reset(ctx context.Context) error {
	session, err := openWFP(false)
	if errors.Is(err, windows.ERROR_SERVICE_NOT_ACTIVE) {
		return nil
	}
	if err != nil {
		return err
	}
	defer session.Close()
	for _, rule := range persistentWindowsRules("") {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := session.DeleteRule(rule.ID); err != nil && !wfpHas(err, windows.FWP_E_FILTER_NOT_FOUND) {
			return err
		}
	}
	if err := session.DeleteSublayer(masirSublayer); err != nil && !wfpHas(err, windows.FWP_E_SUBLAYER_NOT_FOUND) {
		return err
	}
	return nil
}

func openWFP(dynamic bool) (*wf.Session, error) {
	return wf.New(&wf.Options{Name: "Masir Kill Switch", Dynamic: dynamic, TransactionStartTimeout: 5 * time.Second})
}

func wfpHas(err error, code windows.Handle) bool {
	return errors.Is(err, windows.Errno(code))
}

func equal(field wf.FieldID, value any) *wf.Match {
	return &wf.Match{Field: field, Op: wf.MatchTypeEqual, Value: value}
}

func persistentWindowsRules(app string) []*wf.Rule {
	var rules []*wf.Rule
	for i, layer := range windowsLayers {
		slot := uint8(1 + i*8)
		add := func(name string, offset uint8, action wf.Action, weight uint64, conditions ...*wf.Match) {
			rules = append(rules, &wf.Rule{
				ID: wf.RuleID(filterID(slot + offset)), Name: "Masir " + name,
				Layer: layer, Sublayer: masirSublayer, Weight: weight, Conditions: conditions,
				Action: action, Persistent: true,
			})
		}
		add("daemon outbound", 1, wf.ActionPermit, 40, equal(wf.FieldALEAppID, app))
		add("loopback", 2, wf.ActionPermit, 40, &wf.Match{
			Field: wf.FieldFlags, Op: wf.MatchTypeFlagsAllSet, Value: wf.ConditionFlagIsLoopback,
		})
		dhcpAddr, clientPort, serverPort := "255.255.255.255", uint16(68), uint16(67)
		if i == 1 {
			dhcpAddr, clientPort, serverPort = "ff02::1:2", 546, 547
			for j, kind := range []uint16{133, 135, 136} {
				add("IPv6 discovery", uint8(4+j), wf.ActionPermit, 40,
					equal(wf.FieldIPProtocol, uint8(58)), equal(wf.FieldIPLocalPort, kind))
			}
		}
		add("DHCP", 3, wf.ActionPermit, 40,
			equal(wf.FieldIPProtocol, uint8(17)), equal(wf.FieldIPLocalPort, clientPort),
			equal(wf.FieldIPRemotePort, serverPort), equal(wf.FieldIPRemoteAddress, netip.MustParseAddr(dhcpAddr)))
		add("block outside VPN", 0, wf.ActionBlock, 1)
	}
	return rules
}
