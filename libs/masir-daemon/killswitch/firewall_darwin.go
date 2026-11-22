package killswitch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/Gozargah/Masir/libs/masir-core/paths"
)

// The stock macOS ruleset evaluates children of com.apple. Only Masir's child is
// changed here. A PF enable reference and the rules survive daemon replacement.
const anchor = "com.apple/masir-kill-switch"

var (
	tunName = regexp.MustCompile(`^utun[0-9]+$`)
	tokenID = regexp.MustCompile(`^[0-9]+$`)
)

type Firewall struct {
	interfaceName string
	disabled      bool
}

func (f *Firewall) Enable(ctx context.Context, name string) error {
	if f.interfaceName == name && name != "" {
		return nil
	}
	rules, err := firewallRules(name)
	if err != nil {
		return err
	}
	if err := paths.EnsureRootDir(paths.PrivilegedRunDir, 0, 0o755); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	main, err := pfctl(ctx, "", "-s", "rules")
	if err != nil {
		return err
	}
	if !strings.Contains(main, `anchor "com.apple/*"`) {
		return errors.New("the macOS PF ruleset has no com.apple/* anchor")
	}
	if err := enablePF(ctx); err != nil {
		return err
	}
	if _, err := pfctl(ctx, rules, "-a", anchor, "-f", "-"); err != nil {
		return err
	}
	// Existing floating PF states are consulted before the new anchor. Flush
	// them on the first successful activation, so already-open direct flows stop.
	// Masir rules use if-bound states or no state at all on later applies.
	if f.interfaceName == "" {
		if _, err := pfctl(ctx, "", "-F", "states"); err != nil {
			return err
		}
	}
	f.interfaceName, f.disabled = name, false
	return nil
}

func (*Firewall) AllowTunnel(context.Context, string) error { return nil }

func (f *Firewall) Disable(ctx context.Context) error {
	if f.disabled {
		return nil
	}
	if err := Reset(ctx); err != nil {
		return err
	}
	f.interfaceName, f.disabled = "", true
	return nil
}

// Reset removes only Masir's firewall policy. Explicit service removal calls
// this as root. Shutdown and restart deliberately leave the policy active.
func Reset(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	token, err := readToken()
	if err != nil {
		return err
	}
	if token == "" {
		return nil
	}
	if _, err := pfctl(ctx, "", "-a", anchor, "-F", "rules"); err != nil {
		return err
	}
	if _, err := pfctl(ctx, "", "-X", token); err != nil {
		// A reboot or another administrator can already have released the token.
		active, checkErr := hasToken(ctx, token)
		if checkErr != nil || active {
			return err
		}
	}
	return os.Remove(tokenPath())
}

func firewallRules(name string) (string, error) {
	if !tunName.MatchString(name) {
		return "", fmt.Errorf("invalid macOS tunnel interface %q", name)
	}
	return fmt.Sprintf(`pass out quick on lo0 all no state
pass out quick on %s all no state
pass out quick inet proto udp from any port 68 to 255.255.255.255 port 67 keep state (if-bound)
pass out quick inet6 proto ipv6-icmp all icmp6-type { routersol, neighbrsol, neighbradv } no state
pass out quick proto { tcp, udp } from any to any user root keep state (if-bound)
block drop out quick all
`, name), nil
}

func tokenPath() string { return filepath.Join(paths.PrivilegedRunDir, "kill-switch.pf-token") }

func readToken() (string, error) {
	raw, err := os.ReadFile(tokenPath())
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(raw))
	if !tokenID.MatchString(token) {
		return "", errors.New("invalid Masir PF enable token")
	}
	return token, nil
}

func writeToken(token string) error {
	path := tokenPath()
	next := path + ".next"
	defer os.Remove(next)
	if err := os.WriteFile(next, []byte(token+"\n"), 0o600); err != nil {
		return err
	}
	return os.Rename(next, path)
}

func hasToken(ctx context.Context, token string) (bool, error) {
	refs, err := pfctl(ctx, "", "-s", "References")
	if err != nil {
		return false, err
	}
	for _, field := range strings.Fields(refs) {
		if field == token {
			return true, nil
		}
	}
	return false, nil
}

func enablePF(ctx context.Context) error {
	old, err := readToken()
	if err != nil {
		return err
	}
	if old != "" {
		active, err := hasToken(ctx, old)
		if err != nil || active {
			return err
		}
	}
	out, err := pfctl(ctx, "", "-E")
	if err != nil {
		return err
	}
	for _, line := range strings.Split(out, "\n") {
		left, right, ok := strings.Cut(line, ":")
		token := strings.TrimSpace(right)
		if ok && strings.EqualFold(strings.TrimSpace(left), "token") && tokenID.MatchString(token) {
			if err := writeToken(token); err != nil {
				_, releaseErr := pfctl(ctx, "", "-X", token)
				return errors.Join(err, releaseErr)
			}
			return nil
		}
	}
	return errors.New("could not read the macOS PF enable token")
}

func pfctl(ctx context.Context, input string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "/sbin/pfctl", args...)
	cmd.Stdin = strings.NewReader(input)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("pfctl %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	// pfctl -E prints the reference token to stderr. Rule listings are on stdout.
	if len(args) == 1 && args[0] == "-E" {
		return string(out) + "\n" + stderr.String(), nil
	}
	return string(out), nil
}
