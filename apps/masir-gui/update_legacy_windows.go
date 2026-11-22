package main

import (
	"errors"
	"fmt"
	"os/exec"
	"os/user"
	"path/filepath"
	"syscall"
)

func importLegacyDesktop(dir string, authorized func()) error {
	u, err := user.Current()
	if err != nil {
		return err
	}
	cmd := exec.Command(filepath.Join(dir, "masir.exe"), "daemon", "import-desktop", "--from", dir, "--user", u.Uid)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if out, err := cmd.CombinedOutput(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 15 {
			return &elevationError{kind: elevationCancelled, code: exit.ExitCode(), msg: tr("Administrator permission was declined")}
		}
		return fmt.Errorf(tr("offline service upgrade: %s: %w"), out, err)
	}
	if authorized != nil {
		authorized()
	}
	return nil
}
