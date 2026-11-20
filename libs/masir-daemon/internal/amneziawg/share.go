package amneziawg

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"strconv"
	"strings"
)

const maxProfileSize = 1 << 20

type Profile struct {
	Name   string
	Config Config
}

// ParseLink accepts self-contained Amnezia export links. A provider account
// token can only be redeemed by that provider and does not contain a tunnel.
func ParseLink(link string) ([]Profile, error) {
	_, payload, _ := strings.Cut(link, "://")
	payload, fragment, _ := strings.Cut(payload, "#")
	name, _ := url.PathUnescape(fragment)
	if len(payload) > maxProfileSize*2 {
		return nil, errors.New("Amnezia link is too large")
	}
	var body []byte
	for _, enc := range []*base64.Encoding{base64.RawURLEncoding, base64.URLEncoding, base64.RawStdEncoding, base64.StdEncoding} {
		if b, err := enc.DecodeString(payload); err == nil {
			body = b
			break
		}
	}
	if len(body) == 0 || len(body) > maxProfileSize {
		return nil, errors.New("invalid Amnezia link payload")
	}

	text := cleanText(body)
	if !IsConfig(text) && !strings.HasPrefix(text, "{") && strings.HasPrefix(link, "vpn://") && len(body) > 4 {
		// Qt qCompress prefixes the zlib stream with the big-endian output size.
		size := binary.BigEndian.Uint32(body[:4])
		if size > 0 && size <= maxProfileSize {
			if r, err := zlib.NewReader(bytes.NewReader(body[4:])); err == nil {
				raw, err := io.ReadAll(io.LimitReader(r, int64(size)+1))
				_ = r.Close()
				if err != nil || uint32(len(raw)) != size {
					return nil, errors.New("invalid compressed Amnezia profile")
				}
				text = cleanText(raw)
			}
		}
	}
	if IsConfig(text) {
		cfg, err := ParseConfig(text)
		if err != nil {
			return nil, err
		}
		return []Profile{{Name: name, Config: cfg}}, nil
	}
	if strings.HasPrefix(text, "{") {
		return parseBundle([]byte(text), name)
	}
	return nil, errors.New("Amnezia link contains no self-contained profile")
}

func cleanText(body []byte) string {
	return strings.TrimSpace(strings.TrimPrefix(string(body), "\ufeff"))
}

func parseBundle(body []byte, name string) ([]Profile, error) {
	var bundle struct {
		Description string                       `json:"description"`
		DNS1        string                       `json:"dns1"`
		DNS2        string                       `json:"dns2"`
		Containers  []map[string]json.RawMessage `json:"containers"`
	}
	if json.Unmarshal(body, &bundle) != nil {
		return nil, errors.New("invalid Amnezia profile bundle")
	}
	if name == "" {
		name = bundle.Description
	}
	var profiles []Profile
	for _, container := range bundle.Containers {
		for _, kind := range []string{"awg", "amnezia-awg", "awg2", "amnezia-awg2"} {
			var item struct {
				LastConfig json.RawMessage `json:"last_config"`
			}
			if json.Unmarshal(container[kind], &item) != nil {
				continue
			}
			var encoded string
			if json.Unmarshal(item.LastConfig, &encoded) == nil {
				item.LastConfig = []byte(encoded)
			}
			var last struct {
				Config string          `json:"config"`
				MTU    json.RawMessage `json:"mtu"`
			}
			if json.Unmarshal(item.LastConfig, &last) != nil || !IsConfig(last.Config) {
				continue
			}
			conf := strings.NewReplacer("$PRIMARY_DNS", bundle.DNS1, "$SECONDARY_DNS", bundle.DNS2).Replace(last.Config)
			cfg, err := ParseConfig(conf)
			if err != nil {
				continue
			}
			if cfg.MTU == 0 && len(last.MTU) > 0 {
				mtu := strings.Trim(string(last.MTU), `"`)
				cfg.MTU, err = strconv.Atoi(mtu)
				if err != nil {
					continue
				}
			}
			profiles = append(profiles, Profile{Name: name, Config: cfg})
		}
	}
	if len(profiles) == 0 {
		return nil, errors.New("Amnezia link contains no self-contained AmneziaWG profile")
	}
	return profiles, nil
}
