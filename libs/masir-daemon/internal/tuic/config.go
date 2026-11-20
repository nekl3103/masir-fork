package tuic

import (
	"errors"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/gofrs/uuid/v5"
)

const Protocol = "tuic"

type Config struct {
	Address           string   `json:"address"`
	Port              uint16   `json:"port"`
	UUID              string   `json:"uuid"`
	Password          string   `json:"password"`
	ServerName        string   `json:"serverName,omitempty"`
	ALPN              []string `json:"alpn,omitempty"`
	CongestionControl string   `json:"congestionControl,omitempty"`
	UDPRelayMode      string   `json:"udpRelayMode,omitempty"`
	SkipCertVerify    bool     `json:"skipCertVerify,omitempty"`
	ZeroRTTHandshake  bool     `json:"zeroRTTHandshake,omitempty"`
}

func (c Config) Endpoint() string { return net.JoinHostPort(c.Address, strconv.Itoa(int(c.Port))) }

func (c Config) Validate() error {
	if c.Address == "" || c.Port == 0 || strings.ContainsAny(c.Address, " \t\r\n\x00") {
		return errors.New("invalid TUIC endpoint")
	}
	if _, err := uuid.FromString(c.UUID); err != nil || c.Password == "" {
		return errors.New("TUIC v5 requires a UUID and password")
	}
	switch c.CongestionControl {
	case "", "cubic", "new_reno", "bbr":
	default:
		return errors.New("invalid TUIC congestion control")
	}
	switch c.UDPRelayMode {
	case "", "native", "quic":
	default:
		return errors.New("invalid TUIC UDP relay mode")
	}
	return nil
}

func ParseLink(line string) (Config, string, error) {
	u, err := url.Parse(line)
	if err != nil || u.Scheme != Protocol || u.User == nil {
		return Config{}, "", errors.New("invalid TUIC link")
	}
	port, portErr := strconv.ParseUint(u.Port(), 10, 16)
	q := u.Query()
	password, _ := u.User.Password()
	lookup := func(keys ...string) string {
		for _, key := range keys {
			if val := q.Get(key); val != "" {
				return val
			}
		}
		return ""
	}
	flag := func(keys ...string) bool {
		val, _ := strconv.ParseBool(lookup(keys...))
		return val
	}
	if flag("disable_sni", "disable-sni") {
		return Config{}, "", errors.New("TUIC profiles with disabled SNI are unsupported")
	}
	cfg := Config{
		Address: u.Hostname(), Port: uint16(port), UUID: u.User.Username(), Password: password,
		ServerName:        lookup("sni", "server_name", "peer"),
		CongestionControl: lookup("congestion_control", "congestion-controller", "congestion"),
		UDPRelayMode:      lookup("udp_relay_mode", "udp-relay-mode"),
		SkipCertVerify:    flag("allow_insecure", "allowInsecure", "insecure"),
		ZeroRTTHandshake:  flag("zero_rtt_handshake", "reduce_rtt", "reduce-rtt"),
	}
	if alpn := lookup("alpn"); alpn != "" {
		cfg.ALPN = strings.Split(alpn, ",")
	}
	if portErr != nil {
		return Config{}, "", errors.New("invalid TUIC server port")
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, "", err
	}
	return cfg, strings.TrimSpace(u.Fragment), nil
}
