package amneziawg

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"strings"
)

const Protocol = "amneziawg"

type Config struct {
	SecretKey string            `json:"secretKey"`
	Address   []string          `json:"address"`
	DNS       []string          `json:"dns,omitempty"`
	MTU       int               `json:"mtu,omitempty"`
	Options   map[string]string `json:"options,omitempty"`
	Peers     []Peer            `json:"peers"`
}

type Peer struct {
	PublicKey    string   `json:"publicKey"`
	PreSharedKey string   `json:"preSharedKey,omitempty"`
	Endpoint     string   `json:"endpoint"`
	AllowedIPs   []string `json:"allowedIPs"`
	KeepAlive    string   `json:"keepAlive,omitempty"`
}

var optionNames = map[string]string{
	"jc": "jc", "jmin": "jmin", "jmax": "jmax",
	"s1": "s1", "s2": "s2", "s3": "s3", "s4": "s4",
	"h1": "h1", "h2": "h2", "h3": "h3", "h4": "h4",
	"i1": "i1", "i2": "i2", "i3": "i3", "i4": "i4", "i5": "i5",
	"headerprotectionkey":    "header_protection_key",
	"contentpaddingaddition": "content_padding_addition",
	"rekeyaftertime":         "rekey_after_time", "rekeytimeout": "rekey_timeout",
	"rejectaftertime": "reject_after_time", "keepalivetimeout": "keepalive_timeout",
	"maxhandshakeattempts": "max_handshake_attempts",
	"randomtrailers":       "random_trailers", "disablecookies": "disable_cookies",
}

func IsConfig(text string) bool {
	for line := range strings.SplitSeq(text, "\n") {
		line = strings.TrimSpace(line)
		line, _, _ = strings.Cut(line, "#")
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, ";") {
			continue
		}
		return strings.EqualFold(line, "[Interface]")
	}
	return false
}

func ParseConfig(text string) (Config, error) {
	var c Config
	var peer *Peer
	for line := range strings.SplitSeq(text, "\n") {
		line, _, _ = strings.Cut(line, "#")
		line = strings.TrimSpace(line)
		switch strings.ToLower(line) {
		case "", "[interface]":
			continue
		case "[peer]":
			c.Peers = append(c.Peers, Peer{})
			peer = &c.Peers[len(c.Peers)-1]
			continue
		}
		if strings.HasPrefix(line, ";") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			return Config{}, errors.New("invalid AmneziaWG configuration line")
		}
		k, v = strings.ToLower(strings.TrimSpace(k)), strings.TrimSpace(v)
		if peer != nil {
			switch k {
			case "publickey":
				peer.PublicKey = v
			case "presharedkey":
				peer.PreSharedKey = v
			case "endpoint":
				peer.Endpoint = v
			case "allowedips":
				peer.AllowedIPs = append(peer.AllowedIPs, csv(v)...)
			case "persistentkeepalive":
				peer.KeepAlive = v
			}
			continue
		}
		switch k {
		case "privatekey":
			c.SecretKey = v
		case "address":
			c.Address = append(c.Address, csv(v)...)
		case "dns":
			c.DNS = append(c.DNS, csv(v)...)
		case "mtu":
			mtu, err := strconv.Atoi(v)
			if err != nil {
				return Config{}, errors.New("invalid AmneziaWG MTU")
			}
			c.MTU = mtu
		default:
			if name, ok := optionNames[strings.ReplaceAll(k, "_", "")]; ok {
				if c.Options == nil {
					c.Options = make(map[string]string)
				}
				if name == "random_trailers" || name == "disable_cookies" {
					switch strings.ToLower(v) {
					case "on":
						v = "true"
					case "off":
						v = "false"
					}
				}
				c.Options[name] = v
			}
		}
	}
	return c, nil
}

func csv(value string) []string {
	items := strings.Split(value, ",")
	for i := range items {
		items[i] = strings.TrimSpace(items[i])
	}
	return slices.DeleteFunc(items, func(item string) bool { return item == "" })
}

func (c Config) Addresses() ([]netip.Addr, []netip.Addr, error) {
	addresses := make([]netip.Addr, 0, len(c.Address))
	for _, value := range c.Address {
		addr, err := netip.ParseAddr(value)
		if err != nil {
			prefix, err := netip.ParsePrefix(value)
			if err != nil {
				return nil, nil, errors.New("invalid AmneziaWG interface address")
			}
			addr = prefix.Addr()
		}
		addresses = append(addresses, addr)
	}
	if len(addresses) == 0 {
		return nil, nil, errors.New("AmneziaWG interface has no address")
	}
	dns := make([]netip.Addr, 0, len(c.DNS))
	for _, value := range c.DNS {
		// wg-quick allows search domains alongside server IPs.
		if addr, err := netip.ParseAddr(value); err == nil {
			dns = append(dns, addr)
		}
	}
	return addresses, dns, nil
}

func (c Config) Validate() error {
	if _, _, err := c.Addresses(); err != nil {
		return err
	}
	if c.MTU != 0 && (c.MTU < 576 || c.MTU > 65535) {
		return errors.New("invalid AmneziaWG MTU")
	}
	if err := c.validatePackets(); err != nil {
		return err
	}
	if _, err := c.UAPI(); err != nil {
		return err
	}
	for _, peer := range c.Peers {
		host, port, err := net.SplitHostPort(peer.Endpoint)
		p, perr := strconv.ParseUint(port, 10, 16)
		if err != nil || host == "" || perr != nil || p == 0 || strings.ContainsAny(host, "\r\n\x00 \t") {
			return errors.New("invalid AmneziaWG endpoint")
		}
	}
	return nil
}

// UAPI converts only supported keys. Peer endpoints are resolved by the device bind.
func (c Config) UAPI() (string, error) {
	private, err := keyHex(c.SecretKey)
	if err != nil {
		return "", errors.New("invalid AmneziaWG private key")
	}
	if len(c.Peers) == 0 {
		return "", errors.New("AmneziaWG configuration has no peers")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "private_key=%s\n", private)
	for name, value := range c.Options {
		if name == "header_protection_key" {
			value, err = keyHex(value)
			if err != nil {
				return "", errors.New("invalid AmneziaWG header protection key")
			}
		}
		if optionNames[strings.ReplaceAll(name, "_", "")] != name || name == "" || strings.ContainsAny(value, "\r\n\x00") {
			return "", errors.New("invalid AmneziaWG option")
		}
		fmt.Fprintf(&b, "%s=%s\n", name, value)
	}
	for _, peer := range c.Peers {
		public, err := keyHex(peer.PublicKey)
		if err != nil {
			return "", errors.New("invalid AmneziaWG peer public key")
		}
		fmt.Fprintf(&b, "public_key=%s\n", public)
		if peer.PreSharedKey != "" {
			psk, err := keyHex(peer.PreSharedKey)
			if err != nil {
				return "", errors.New("invalid AmneziaWG preshared key")
			}
			fmt.Fprintf(&b, "preshared_key=%s\n", psk)
		}
		if strings.ContainsAny(peer.Endpoint, "\r\n\x00") {
			return "", errors.New("invalid AmneziaWG endpoint")
		}
		fmt.Fprintf(&b, "endpoint=%s\n", peer.Endpoint)
		if len(peer.AllowedIPs) == 0 {
			return "", errors.New("AmneziaWG peer has no allowed IPs")
		}
		for _, ip := range peer.AllowedIPs {
			prefix, err := netip.ParsePrefix(ip)
			if err != nil {
				return "", errors.New("invalid AmneziaWG allowed IP")
			}
			fmt.Fprintf(&b, "allowed_ip=%s\n", prefix.Masked())
		}
		if peer.KeepAlive != "" {
			a, bEnd, ranged := strings.Cut(peer.KeepAlive, "-")
			lo, err := strconv.ParseUint(a, 10, 16)
			if err != nil {
				return "", errors.New("invalid AmneziaWG persistent keepalive")
			}
			if ranged {
				hi, err := strconv.ParseUint(bEnd, 10, 16)
				if err != nil || hi < lo {
					return "", errors.New("invalid AmneziaWG persistent keepalive range")
				}
			}
			fmt.Fprintf(&b, "persistent_keepalive_interval=%s\n", peer.KeepAlive)
		}
	}
	return b.String(), nil
}

// The upstream UAPI parses lengths as integers. Ensure a provider's profile
// cannot request negative slices, oversized packets, or unbounded allocations.
func (c Config) validatePackets() error {
	const maxDatagram = 65507
	count, err := c.optionUint("jc", 128)
	if err != nil {
		return err
	}
	minimum, err := c.optionUint("jmin", maxDatagram)
	if err != nil {
		return err
	}
	maximum, err := c.optionUint("jmax", maxDatagram)
	if err != nil {
		return err
	}
	if count > 0 && minimum > maximum {
		return errors.New("invalid AmneziaWG junk packet range")
	}

	for i, overhead := range []uint64{148, 92, 64, uint64(max(c.MTU, 1420)) + 32} {
		if overhead > maxDatagram {
			return errors.New("invalid AmneziaWG MTU")
		}
		if _, err := c.optionUint("s"+strconv.Itoa(i+1), maxDatagram-overhead); err != nil {
			return err
		}
	}
	for _, field := range []string{"i1", "i2", "i3", "i4", "i5"} {
		left := c.Options[field]
		size := uint64(0)
		for {
			_, rest, ok := strings.Cut(left, "<")
			if !ok {
				break
			}
			tag, tail, closed := strings.Cut(rest, ">")
			if !closed {
				return fmt.Errorf("invalid AmneziaWG %s", field)
			}
			left = tail
			parts := strings.Fields(tag)
			if len(parts) < 1 {
				return fmt.Errorf("invalid AmneziaWG %s", field)
			}
			if parts[0] == "r" || parts[0] == "rc" || parts[0] == "rd" {
				if len(parts) < 2 {
					return fmt.Errorf("invalid AmneziaWG %s length", field)
				}
				n, err := strconv.ParseUint(parts[1], 10, 16)
				if err != nil {
					return fmt.Errorf("invalid AmneziaWG %s length", field)
				}
				size += n
			}
			if parts[0] == "b" && len(parts) > 1 {
				size += uint64(len(strings.TrimPrefix(parts[1], "0x"))) / 2
			}
			if parts[0] == "t" || parts[0] == "dz" {
				size += 4
			}
			if size > maxDatagram {
				return fmt.Errorf("AmneziaWG %s exceeds the UDP packet size", field)
			}
		}
	}
	return nil
}

func (c Config) optionUint(field string, maxValue uint64) (uint64, error) {
	value, ok := c.Options[field]
	if !ok {
		return 0, nil
	}
	n, err := strconv.ParseUint(value, 10, 16)
	if err != nil || n > maxValue {
		return 0, fmt.Errorf("invalid AmneziaWG %s", field)
	}
	return n, nil
}

func keyHex(s string) (string, error) {
	for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding} {
		if data, err := encoding.DecodeString(s); err == nil && len(data) == 32 {
			return hex.EncodeToString(data), nil
		}
	}
	if data, err := hex.DecodeString(s); err == nil && len(data) == 32 {
		return hex.EncodeToString(data), nil
	}
	return "", errors.New("expected a 32-byte key")
}
