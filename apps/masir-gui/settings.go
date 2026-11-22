package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	pb "github.com/Gozargah/Masir/libs/masir-proto/gen/masir/daemon/v1"
	"github.com/egoist/mygo"
	"google.golang.org/protobuf/proto"
)

// settings.go holds what the settings page does: the fields the daemon fills, the calls
// that save them, and the login and port actions. settings_view.go draws the page.

// defaultGateway is the tunnel's out-of-the-box address, as the daemon defines it.
const defaultGateway = "10.10.20.1/24"

// enterSettings refreshes what the settings page shows as it opens.
func (a *app) enterSettings() {
	a.refreshCLIState()     // check the command-line tool in the background
	a.refreshLoginState()   // the login item
	a.refreshServiceState() // and the service, which may be stopped
	a.syncTextSettings(a.status.GetSettings())
	a.markAnalyticsNotice()
}

// syncTextSettings puts the daemon's values in the settings fields. It is called when the
// page opens and while it is closed, never in the middle of typing.
func (a *app) syncTextSettings(s *pb.Settings) {
	if s == nil {
		return
	}
	a.portText = fmt.Sprint(s.GetProxyPort())
	a.dns = textSetting{text: strings.Join(s.GetDnsServers(), ", ")}
	a.tunDNS = textSetting{text: strings.Join(s.GetTunDns(), ", ")}
}

// markAnalyticsNotice records that the app has shown the analytics notice, as the
// command line does. The daemon sends nothing before this marker exists.
func (a *app) markAnalyticsNotice() {
	if a.noticeMarked {
		return
	}
	a.noticeMarked = true
	a.async(markNoticeShown)
}

// openXrayConfig writes the configuration the daemon is running to a temporary file and
// opens it, as `masir get-config` prints it.
func (a *app) openXrayConfig() {
	a.async(func() {
		res, err := callRPC(func(ctx context.Context) (*pb.GetXrayConfigResponse, error) {
			return a.d.GetXrayConfig(ctx, &pb.GetXrayConfigRequest{})
		})
		if err != nil {
			a.post(func() { a.fail(tr("Couldn't read the config: "), err) })
			return
		}
		if res.GetJson() == "" {
			a.post(func() { a.notifyWarning(tr("Xray isn't running, so there's no config yet")) })
			return
		}
		var pretty bytes.Buffer
		if json.Indent(&pretty, []byte(res.GetJson()), "", "  ") != nil {
			pretty.Reset()
			pretty.WriteString(res.GetJson())
		}
		pretty.WriteByte('\n')
		path := filepath.Join(os.TempDir(), "masir-xray-config.json")
		if err := os.WriteFile(path, pretty.Bytes(), 0o600); err != nil {
			a.post(func() { a.fail(tr("Couldn't write the config: "), err) })
			return
		}
		if err := openFileIn(path); err != nil {
			a.post(func() { a.fail(tr("Couldn't open the config: "), err) })
		}
	})
}

// ---- saving the text settings ----

// applyTextSetting saves one settings field. When the daemon refuses it, the field goes
// back to what the daemon has and a toast says why.
func (a *app) applyTextSetting(f *textSetting, restore string, req *pb.UpdateSettingsRequest) {
	a.mutate(tr("Couldn't save the setting: "), nil, func() { f.text = restore }, func(ctx context.Context) error {
		_, err := a.d.UpdateSettings(ctx, req)
		return err
	})
}

// saveBootstrapDNS saves the plain resolvers used for proxy server names. At least one is
// required; the daemon refuses an empty list.
func (a *app) saveBootstrapDNS(text string) {
	a.dns.edited = false
	list, err := parseIPList(text)
	if err != nil || len(list) == 0 {
		if err == nil {
			a.notifyWarning(tr("Bootstrap DNS needs at least one address"))
		} else {
			a.notifyWarning(err.Error())
		}
		a.dns.text = strings.Join(a.status.GetSettings().GetDnsServers(), ", ")
		return
	}
	a.dns.text = strings.Join(list, ", ")
	saved := a.status.GetSettings().GetDnsServers()
	if sameList(list, saved) {
		return
	}
	a.applyTextSetting(&a.dns, strings.Join(saved, ", "), &pb.UpdateSettingsRequest{DnsServers: &pb.StringList{Values: list}})
}

// saveTunDNS saves the resolvers the tunnel advertises; an empty field means the bootstrap
// DNS.
func (a *app) saveTunDNS(text string) {
	a.tunDNS.edited = false
	list, err := parseIPList(text)
	if err != nil {
		a.notifyWarning(err.Error())
		a.tunDNS.text = strings.Join(a.status.GetSettings().GetTunDns(), ", ")
		return
	}
	a.tunDNS.text = strings.Join(list, ", ")
	saved := a.status.GetSettings().GetTunDns()
	if sameList(list, saved) {
		return
	}
	a.applyTextSetting(&a.tunDNS, strings.Join(saved, ", "), &pb.UpdateSettingsRequest{TunDns: &pb.StringList{Values: list}})
}

// parseIPList reads a comma- or space-separated list of IP addresses.
func parseIPList(text string) ([]string, error) {
	var out []string
	for _, f := range strings.FieldsFunc(text, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n'
	}) {
		if net.ParseIP(f) == nil {
			return nil, fmt.Errorf(tr("%s is not an IP address"), f)
		}
		out = append(out, f)
	}
	return out, nil
}

// sameList reports whether two lists of addresses are the same, in any order.
func sameList(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]int, len(a))
	for _, s := range a {
		seen[s]++
	}
	for _, s := range b {
		seen[s]--
		if seen[s] < 0 {
			return false
		}
	}
	return true
}

// restoreDefaults puts back what the daemon says each setting is when it isn't
// customized. Power, the privacy choice, and the rules stay as the person left them.
func (a *app) restoreDefaults() {
	a.async(func() {
		res, err := callRPC(func(ctx context.Context) (*pb.GetSettingsResponse, error) {
			return a.d.GetSettings(ctx, &pb.GetSettingsRequest{})
		})
		if err != nil {
			a.post(func() { a.fail(tr("Couldn't load the defaults: "), err) })
			return
		}
		d := res.GetDefaults()
		req := &pb.UpdateSettingsRequest{
			AlwaysOnProxy:      proto.Bool(d.GetAlwaysOnProxy()),
			AllowLan:           proto.Bool(d.GetAllowLan()),
			ProxyPort:          proto.Uint32(d.GetProxyPort()),
			DohDns:             proto.String(d.GetDohDns()),
			DnsServers:         &pb.StringList{Values: append([]string(nil), d.GetDnsServers()...)},
			TunDns:             &pb.StringList{Values: append([]string(nil), d.GetTunDns()...)},
			TunGateway:         proto.String(d.GetTunGateway()),
			SubIntervalMinutes: proto.Uint32(d.GetSubIntervalMinutes()),
			XrayLogLevel:       proto.String(d.GetXrayLogLevel()),
		}
		a.post(func() {
			a.mutate(tr("Couldn't restore the defaults: "), func() {
				a.portText, a.portEdited = fmt.Sprint(d.GetProxyPort()), false
				a.dns.text = strings.Join(d.GetDnsServers(), ", ")
				a.tunDNS.text = strings.Join(d.GetTunDns(), ", ")
				a.notifySuccess(tr("Settings restored to their defaults"))
			}, nil, func(ctx context.Context) error {
				_, err := a.d.UpdateSettings(ctx, req)
				return err
			})
		})
	})
}

// loginEnabled is whether the app starts at login. The system is asked once, not once a
// frame: the answer comes from the OS and is not instant.
func (a *app) loginEnabled() bool {
	if !a.loginChecked {
		a.loginChecked = true
		a.loginOn = openAtLogin()
	}
	return a.loginOn
}

// refreshLoginState asks the system for the login item in the background, so the settings
// page draws at once and catches up if it changed elsewhere.
func (a *app) refreshLoginState() {
	a.async(func() {
		on := openAtLogin()
		a.post(func() {
			if on {
				a.loginNeedsApproval = false
			}
			if a.loginChecked && a.loginOn == on {
				return
			}
			a.loginOn, a.loginChecked = on, true
		})
	})
}

func (a *app) setLoginEnabled(on bool) {
	err := setOpenAtLogin(on)
	if err != nil && on {
		// A login item left behind by an earlier copy of Masir can make register fail
		// ("already registered"). Removing it and registering again is what the person
		// did by hand; do it for them, once.
		if offErr := setOpenAtLogin(false); offErr == nil {
			err = setOpenAtLogin(true)
		}
	}
	if err != nil {
		a.loginNeedsApproval = strings.Contains(err.Error(), "Login Items")
		a.fail(tr("Couldn't change it: "), err)
		return
	}
	a.loginNeedsApproval = false
	a.loginOn, a.loginChecked = on, true
}

// digitsOnly keeps the digits of s, capped at five: a port field that takes a number
// and nothing else.
func digitsOnly(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' && b.Len() < 5 {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// applyPort saves the port field, or puts the saved port back when it isn't a port.
func (a *app) applyPort() {
	a.portEdited = false
	n, err := strconv.Atoi(a.portText)
	if err != nil || n < 1024 || n > 65535 {
		a.notifyWarning(tr("The port must be a number from 1024 to 65535"))
		a.portText = fmt.Sprint(a.status.GetSettings().GetProxyPort())
		return
	}
	port := uint32(n)
	if port == a.status.GetSettings().GetProxyPort() {
		return
	}
	oldPort := fmt.Sprint(a.status.GetSettings().GetProxyPort())
	a.mutate(tr("Couldn't save the setting: "), nil, func() { a.portText = oldPort }, func(ctx context.Context) error {
		_, err := a.d.UpdateSettings(ctx, &pb.UpdateSettingsRequest{ProxyPort: &port})
		return err
	})
}

// guiSettingsFile is where GUI-only preferences are saved, beside panel-height and cli-install.
func guiSettingsFile() string {
	dir, err := mygo.App.Path(mygo.PathUserData)
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "gui-settings.json")
}

// guiSettings holds GUI-only preferences that are not managed by the daemon.
type guiSettings struct {
	UnitPending            bool     `json:"unit_pending,omitempty"`
	DisconnectOnQuit       *bool    `json:"disconnect_on_quit,omitempty"`
	CollapsedSubscriptions []string `json:"collapsed_subscriptions,omitempty"`
	CollapsedRuleSections  []string `json:"collapsed_rule_sections,omitempty"`
	ServerScrollKey        string   `json:"server_scroll_key,omitempty"`
	ServerScrollRow        int      `json:"server_scroll_row,omitempty"`
	RuleScrollKey          string   `json:"rule_scroll_key,omitempty"`
	RuleScrollRow          int      `json:"rule_scroll_row,omitempty"`
}

func (a *app) currentGUISettings() guiSettings {
	val := a.disconnectQuit
	var collapsed []string
	for id, c := range a.collapsedSubs {
		if c {
			collapsed = append(collapsed, id)
		}
	}
	sort.Strings(collapsed)
	var collapsedRules []string
	for title, c := range a.collapsedRules {
		if c {
			collapsedRules = append(collapsedRules, title)
		}
	}
	sort.Strings(collapsedRules)
	return guiSettings{
		UnitPending:            a.updates.state.unitPending,
		DisconnectOnQuit:       &val,
		CollapsedSubscriptions: collapsed,
		CollapsedRuleSections:  collapsedRules,
		ServerScrollKey:        a.serverScrollKey,
		ServerScrollRow:        a.serverScrollRow,
		RuleScrollKey:          a.ruleScrollKey,
		RuleScrollRow:          a.ruleScrollRow,
	}
}

func (a *app) loadGUISettings() {
	if a.guiSettingsFile == "" {
		return
	}
	a.guiSettingsMu.Lock()
	defer a.guiSettingsMu.Unlock()
	b, err := os.ReadFile(a.guiSettingsFile)
	if err != nil {
		return
	}
	var s guiSettings
	if err := json.Unmarshal(b, &s); err != nil {
		return
	}
	a.updates.state.unitPending = s.UnitPending
	if s.DisconnectOnQuit != nil {
		a.disconnectQuit = *s.DisconnectOnQuit
	}
	if len(s.CollapsedSubscriptions) > 0 {
		if a.collapsedSubs == nil {
			a.collapsedSubs = make(map[string]bool)
		}
		for _, id := range s.CollapsedSubscriptions {
			a.collapsedSubs[id] = true
		}
	}
	if len(s.CollapsedRuleSections) > 0 {
		if a.collapsedRules == nil {
			a.collapsedRules = make(map[string]bool)
		}
		for _, title := range s.CollapsedRuleSections {
			a.collapsedRules[title] = true
		}
	}
	if s.ServerScrollKey != "" {
		a.serverScrollKey = s.ServerScrollKey
	}
	if s.ServerScrollRow > 0 {
		a.serverScrollRow = s.ServerScrollRow
	}
	if s.RuleScrollKey != "" {
		a.ruleScrollKey = s.RuleScrollKey
	}
	if s.RuleScrollRow > 0 {
		a.ruleScrollRow = s.RuleScrollRow
	}
}

func (a *app) saveGUISettings() {
	if a.guiSettingsFile == "" {
		return
	}
	path := a.guiSettingsFile
	s := a.currentGUISettings()
	n := a.guiSettingsWrites.Add(1)
	a.async(func() {
		a.guiSettingsMu.Lock()
		defer a.guiSettingsMu.Unlock()
		if a.guiSettingsWrites.Load() != n {
			return
		}
		data, err := json.MarshalIndent(s, "", "  ")
		if err != nil {
			log.Printf("gui settings: %v", err)
			return
		}
		data = append(data, '\n')
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			log.Printf("gui settings: %v", err)
			return
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			log.Printf("gui settings: %v", err)
		}
	})
}

func (a *app) flushGUISettings() {
	if a.guiSettingsFile == "" {
		return
	}
	a.guiSettingsWrites.Add(1)
	path := a.guiSettingsFile
	s := a.currentGUISettings()
	a.guiSettingsMu.Lock()
	defer a.guiSettingsMu.Unlock()
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	_ = os.WriteFile(path, data, 0o600)
}

func (a *app) disconnectOnQuitEnabled() bool {
	return a.disconnectQuit
}

func (a *app) setDisconnectOnQuit(on bool) {
	if a.disconnectQuit == on {
		return
	}
	a.disconnectQuit = on
	a.saveGUISettings()
}
