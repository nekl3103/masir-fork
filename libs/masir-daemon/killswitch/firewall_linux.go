//go:build linux && !android

package killswitch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

const nftTable = "masir_kill_switch"

var linuxInterface = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,15}$`)

type Firewall struct {
	interfaceName string
	disabled      bool
}

func (f *Firewall) Enable(ctx context.Context, name string) error {
	if f.interfaceName == name && name != "" {
		return nil
	}
	rules, err := linuxRules(name)
	if err != nil {
		return err
	}
	nft, err := findNft()
	if err != nil {
		return fmt.Errorf("Kill Switch requires nftables (nft): %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	exists, err := tableExists(ctx, nft)
	if err != nil {
		return err
	}
	if exists {
		rules = "delete table inet " + nftTable + "\n" + rules
	}
	if _, err := nftCommand(ctx, nft, rules, "-f", "-"); err != nil {
		return err
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

func Reset(ctx context.Context) error {
	nft, err := findNft()
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	exists, err := tableExists(ctx, nft)
	if err != nil || !exists {
		return err
	}
	_, err = nftCommand(ctx, nft, "", "delete", "table", "inet", nftTable)
	return err
}

func linuxRules(name string) (string, error) {
	if !linuxInterface.MatchString(name) {
		return "", fmt.Errorf("invalid Linux tunnel interface %q", name)
	}
	// All operations in the batch commit together. A later firewall chain cannot
	// turn a drop here into an accept. oifname works before the TUN is created.
	// The privileged daemon owns the actual outbound sockets, including explicit
	// direct routes. The guard does not exempt any unprivileged process by address.
	return fmt.Sprintf(`table inet %[1]s {
  chain output {
    type filter hook output priority -200; policy drop;
    oifname { "lo", "%[2]s" } accept
    ip daddr 255.255.255.255 udp sport 68 udp dport 67 accept
    ip6 daddr ff02::1:2 udp sport 546 udp dport 547 accept
    ip6 hoplimit 255 icmpv6 type { nd-router-solicit, nd-neighbor-solicit, nd-neighbor-advert } accept
    meta skuid 0 meta l4proto { tcp, udp } accept
  }
}
`, nftTable, name), nil
}

func tableExists(ctx context.Context, nft string) (bool, error) {
	raw, err := nftCommand(ctx, nft, "", "-j", "list", "tables")
	if err != nil {
		return false, err
	}
	var listed struct {
		Items []struct {
			Table struct {
				Family string
				Name   string
			}
		} `json:"nftables"`
	}
	if err := json.Unmarshal(raw, &listed); err != nil {
		return false, fmt.Errorf("read nftables tables: %w", err)
	}
	for _, entry := range listed.Items {
		if entry.Table.Family == "inet" && entry.Table.Name == nftTable {
			return true, nil
		}
	}
	return false, nil
}

func findNft() (string, error) {
	for _, path := range []string{"/usr/sbin/nft", "/usr/bin/nft", "/sbin/nft", "/bin/nft"} {
		info, err := os.Stat(path)
		if err == nil && info.Mode().IsRegular() && info.Mode()&0o111 != 0 {
			return path, nil
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
	}
	return "", os.ErrNotExist
}

func nftCommand(ctx context.Context, path, input string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Stdin = strings.NewReader(input)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("nftables: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return out, nil
}
