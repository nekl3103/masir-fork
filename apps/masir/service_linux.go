package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"

	"github.com/Gozargah/Masir/libs/masir-core/service"
	"github.com/Gozargah/Masir/libs/masir-daemon/killswitch"
)

// Restart=on-failure: a clean exit (Shutdown) leaves it stopped, a crash brings it back.
//
// The sandbox leaves the daemon three capabilities and the network: CHOWN and DAC_OVERRIDE
// to create the served user's files and hand them over, and NET_ADMIN for the TUN device
// and its routes. Tested against a systemd service on Linux: without NET_ADMIN Xray can't
// create the tunnel, and without the other two a fresh ~/.config/masir can't be made.
const unitTemplate = `[Unit]
Description=masir daemon
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=%s daemon run --user %d
Restart=on-failure
# The daemon ends its streams and exits on SIGTERM; this only bounds a stop that can't
# finish (a wedged Xray, say), so systemctl stop never waits systemd's 90s default.
TimeoutStopSec=10
NoNewPrivileges=yes
PrivateTmp=yes
ProtectSystem=full
ProtectControlGroups=yes
ProtectKernelModules=yes
CapabilityBoundingSet=CAP_CHOWN CAP_DAC_OVERRIDE CAP_NET_ADMIN
# /usr is read-only under ProtectSystem=full. The daemon replaces its own binary and the
# geo data beside it when it updates, so that directory stays writable.
ReadWritePaths=-%s

[Install]
WantedBy=multi-user.target
`

func serviceUnit(bin string, uid int) string {
	return fmt.Sprintf(unitTemplate, bin, uid, service.InstallDir())
}

// serviceDefinition is the file systemd reads for bin serving uid.
func serviceDefinition(bin string, uid int) string { return serviceUnit(bin, uid) }

// applyServiceDefinition writes text to the unit and reloads systemd. It restarts the
// service when the file changed, so a new sandbox (capabilities, ProtectSystem) applies
// now instead of at some later reinstall. It must run as root, outside the unit.
func applyServiceDefinition(path, text string, retry bool) error {
	_, err := applyLinuxServiceDefinition(path, text, retry)
	return err
}

func applyLinuxServiceDefinition(path, text string, retry bool) (restarted bool, err error) {
	changed, err := writeServiceDefinition(path, text)
	if err != nil || (!changed && !retry) {
		return false, err
	}
	if err := run("systemctl", "daemon-reload"); err != nil {
		return false, err
	}
	if exec.Command("systemctl", "is-active", "--quiet", "masird.service").Run() == nil {
		err := run("systemctl", "restart", "masird.service")
		return err == nil, err
	}
	return false, nil
}

// writeServiceDefinition writes text only when it differs, and reports whether it did.
func writeServiceDefinition(path, text string) (bool, error) {
	old, _ := os.ReadFile(path)
	if string(old) == text {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	return true, os.WriteFile(path, []byte(text), 0o644)
}

func serviceDefinitionPath() string { return service.DefinitionPath() }

func installService(bin, user string) error {
	uid, err := strconv.Atoi(user)
	if err != nil {
		return fail("the service's user must be a uid, not %q", user)
	}
	if err := os.WriteFile(service.DefinitionPath(), []byte(serviceUnit(bin, uid)), 0o644); err != nil {
		return err
	}
	if err := run("systemctl", "daemon-reload"); err != nil {
		return err
	}
	if err := run("systemctl", "enable", "masird.service"); err != nil {
		return err
	}
	return run("systemctl", "restart", "masird.service") // `enable --now` wouldn't restart an older running copy
}

func uninstallService() error {
	_ = exec.Command("systemctl", "disable", "--now", "masird.service").Run()
	if err := killswitch.Reset(context.Background()); err != nil {
		return fmt.Errorf("disable Kill Switch: %w", err)
	}
	if err := os.Remove(service.DefinitionPath()); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	// Drop the fcontext rule labelServiceBinary added; best effort — semanage may be
	// absent, and the rule names a path this removal is about to empty anyway.
	_ = exec.Command("semanage", "fcontext", "-d", service.BinaryPath()).Run()
	return run("systemctl", "daemon-reload")
}

func restartService() error {
	return sudo("systemctl", "restart", "masird.service")
}
