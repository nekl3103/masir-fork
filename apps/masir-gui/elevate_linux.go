//go:build linux

package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// Linux asks for root through polkit: pkexec shows the desktop's own password dialog.
// The daemon itself runs as the user for proxy mode; only TUN and the service need this.

func elevateDaemon(bin string) error {
	if bin == "" {
		return errors.New(tr("no masir executable to start the daemon from"))
	}
	abs, err := filepath.Abs(bin)
	if err != nil {
		return err
	}
	// The shell puts the daemon in the background, so pkexec returns as soon as the user
	// has granted permission, and the daemon outlives it.
	script := fmt.Sprintf("%s daemon run --user %s >/dev/null 2>&1 &", shellQuote(abs), strconv.Itoa(os.Getuid()))
	if _, err := pkexec("/bin/sh", "-c", script); err != nil {
		return err
	}
	return nil
}

// installService installs the daemon as the systemd service through pkexec, so TUN needs
// no prompt from then on.
func installService(bin string) error {
	if bin == "" {
		return errors.New(tr("no masir executable to install the service from"))
	}
	abs, err := filepath.Abs(bin)
	if err != nil {
		return err
	}
	if _, err := pkexec(abs, "daemon", "enable", "--user", strconv.Itoa(os.Getuid())); err != nil {
		return err
	}
	return nil
}

func uninstallService(bin string) error {
	if bin == "" {
		return errors.New(tr("no masir executable to remove the service with"))
	}
	abs, err := filepath.Abs(bin)
	if err != nil {
		return err
	}
	// --user names the user the service serves, as enable does: the removal waits for
	// that user's ports, and pkexec records no SUDO_UID for masir to fall back on.
	if _, err := pkexec(abs, "daemon", "disable", "--user", strconv.Itoa(os.Getuid())); err != nil {
		return err
	}
	return nil
}

// startService starts the installed systemd unit again. The app never starts a second
// daemon of its own while a service is installed.
func startService(bin string) error {
	if bin == "" {
		return errors.New(tr("no masir executable to start the service with"))
	}
	abs, err := filepath.Abs(bin)
	if err != nil {
		return err
	}
	_, err = pkexec(abs, "daemon", "restart")
	return err
}

// openLoginItems has no equivalent on Linux: systemd's enablement is the setting.
func openLoginItems() error { return nil }

// pkexec runs a command as root through the desktop's polkit prompt.
func pkexec(argv ...string) ([]byte, error) { return pkexecAnnounced(nil, argv...) }

// pkexecAnnounced is pkexec for a helper that prints the line `authorized` once it runs
// as root: authorized is called then, at most once, while the helper still works. That
// line is a signal, not output. stdout and stderr share one pipe, as CombinedOutput did.
func pkexecAnnounced(authorized func(), argv ...string) ([]byte, error) {
	if _, err := exec.LookPath("pkexec"); err != nil {
		return nil, &elevationError{kind: elevationUnavailable, msg: tr("this desktop has no pkexec to ask for root; run `sudo masir daemon enable` once")}
	}
	cmd := exec.Command("pkexec", argv...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	r, w, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	defer r.Close()
	cmd.Stdout, cmd.Stderr = w, w
	err = cmd.Start()
	w.Close()
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	announced := false
	br := bufio.NewReader(r)
	for {
		line, rerr := br.ReadString('\n')
		if !announced && strings.TrimSpace(line) == "authorized" {
			announced = true
			if authorized != nil {
				authorized()
			}
		} else {
			out.WriteString(line)
		}
		if rerr != nil {
			break
		}
	}
	if err := cmd.Wait(); err != nil {
		msg := strings.TrimSpace(out.String())
		if msg == "" {
			msg = err.Error()
		}
		code := -1
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		}
		return out.Bytes(), classifyPkexec(code, msg)
	}
	return out.Bytes(), nil
}

// classifyPkexec reads pkexec's exit: 126 is a dismissed dialog, 127 a refusal or, by
// its message, no authentication agent ("No authentication agent found", or "Error
// creating textual authentication agent" when its own fallback has no terminal, as
// under Setsid); anything else is the helper's own status.
func classifyPkexec(exit int, output string) *elevationError {
	e := &elevationError{kind: elevationFailed, code: exit, msg: "root permission wasn't granted: " + output}
	switch {
	case exit == 126:
		e.kind, e.msg = elevationCancelled, tr("permission wasn't granted")
	case exit == 127 && strings.Contains(output, "authentication agent"):
		e.kind = elevationUnavailable
	case exit == 127:
		e.kind = elevationDenied
	}
	return e
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
