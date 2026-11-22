// Package mobile exposes Masir's connection engine to Android's VpnService.
package mobile

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"runtime/debug"
	"strings"
	"time"

	"github.com/Gozargah/Masir/libs/masir-daemon/internal/compose"
	"github.com/Gozargah/Masir/libs/masir-daemon/internal/store"
	"github.com/Gozargah/Masir/libs/masir-daemon/internal/subs"
	"github.com/Gozargah/Masir/libs/masir-daemon/internal/xray"
)

const maxSubscription = 10 << 20

// ImportProfiles accepts a share link, a group of links, a configuration or an HTTPS subscription.
func ImportProfiles(input string) (string, error) {
	text := strings.TrimSpace(input)
	if u, err := url.Parse(text); err == nil && u.Host != "" && (u.Scheme == "http" || u.Scheme == "https") {
		if u.Scheme != "https" {
			return "", errors.New("the subscription must use HTTPS")
		}
		raw, err := downloadProfiles(text)
		if err != nil {
			return "", err
		}
		text = string(raw)
	}
	if len(text) > maxSubscription {
		return "", errors.New("the subscription exceeds 10 MiB")
	}
	configs, err := subs.Parse([]byte(text))
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(configs)
	return string(data), err
}

func downloadProfiles(endpoint string) ([]byte, error) {
	transport := &http.Transport{DialContext: subscriptionDial, DisableKeepAlives: true}
	client := &http.Client{
		Timeout: 20 * time.Second, Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if req.URL.Scheme != "https" || len(via) >= 10 {
				return errors.New("invalid subscription redirect")
			}
			return nil
		},
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, errors.New("invalid subscription address")
	}
	req.Header.Set("User-Agent", "v2rayNG/2.3.11")
	resp, err := client.Do(req)
	if err != nil {
		return nil, errors.New("could not download the subscription")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, errors.New("the subscription server refused the request")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxSubscription+1))
	if len(data) > maxSubscription {
		return nil, errors.New("the subscription exceeds 10 MiB")
	}
	return data, err
}

// Configured rules are plain domain names or Masir's existing domain matchers,
// with one matcher per line. The split route is part of the regular Xray policy.
func connectionProfile(profile, directDomains string, strict bool) (*xray.Profile, error) {
	var cfg subs.Config
	if err := json.Unmarshal([]byte(profile), &cfg); err != nil || len(cfg.Outbound) == 0 && cfg.Profile == nil {
		return nil, errors.New("select a VPN server")
	}
	settings := store.DefaultSettings()
	settings.KillSwitch = strict
	st := store.State{Settings: settings}
	var domains []string
	for line := range strings.SplitSeq(directDomains, "\n") {
		if match := strings.TrimSpace(line); match != "" {
			domains = append(domains, match)
		}
	}
	if len(domains) > 0 {
		rule, err := json.Marshal(map[string][]string{"domain": domains})
		if err != nil {
			return nil, err
		}
		if err := st.AddRule("android-direct", rule, store.Target{Kind: store.TargetDirect}, false); err != nil {
			return nil, err
		}
	}
	out, err := compose.Build(compose.Input{State: st, Tun: true, Active: cfg.Outbound, ActiveProfile: cfg.Profile})
	if err != nil {
		return nil, err
	}
	if err := xray.CheckNoLocalFiles(out.Config); err != nil {
		return nil, err
	}
	return xray.ParseProfile(out.Config)
}

// AmneziaWGVersion reports the library linked into this mobile engine.
func AmneziaWGVersion() string {
	info, ok := debug.ReadBuildInfo()
	if ok {
		for _, dep := range info.Deps {
			if dep.Path == "github.com/amnezia-vpn/amneziawg-go/v3" {
				version := dep.Version
				if dep.Replace != nil {
					version = dep.Replace.Version
				}
				return strings.TrimPrefix(version, "v")
			}
		}
	}
	return "—"
}
