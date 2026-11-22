package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/Gozargah/Masir/libs/masir-core/service"
	"github.com/Gozargah/Masir/libs/masir-daemon/killswitch"
)

// The service exits 0 only when asked to (Shutdown), so KeepAlive restarts it after a
// crash but leaves it stopped after `masir daemon stop`. RunAtLoad brings it up at boot.
const plistTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>%s</string>
  <!-- Login Items groups a helper with its app when the Team ID matches. This build
       is ad-hoc (no Team ID), so the executable itself carries the name and Finder
       icon: it is installed as Masir with AppIcon.rsrc/FinderInfo.bin. -->
  <key>AssociatedBundleIdentifiers</key>
  <array><string>com.masir.app</string></array>
  <key>ProgramArguments</key>
  <array>
    <string>%s</string>
    <string>daemon</string>
    <string>run</string>
    <string>--user</string>
    <string>%d</string>
  </array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key>
  <dict><key>SuccessfulExit</key><false/></dict>
  <key>StandardOutPath</key><string>/var/log/masird.log</string>
  <key>StandardErrorPath</key><string>/var/log/masird.log</string>
</dict>
</plist>
`

func servicePlist(bin string, uid int) string {
	return fmt.Sprintf(plistTemplate, service.Label, bin, uid)
}

// serviceDefinition is the file launchd reads for bin serving uid.
func serviceDefinition(bin string, uid int) string { return servicePlist(bin, uid) }

// applyServiceDefinition writes text to the plist and reloads launchd, so a changed
// definition applies now instead of at the next login. It must run as root.
func applyServiceDefinition(path, text string, _ bool) error {
	old, _ := os.ReadFile(path)
	if string(old) == text {
		return nil
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		return err
	}
	// A provenance xattr makes launchd refuse the job; see installService.
	_ = exec.Command("xattr", "-c", path).Run()
	// bootout waits for launchd to release the label and bootstrap retries error 5:
	// launchctl returns while the old daemon is still exiting.
	bootout()
	return bootstrap()
}

func serviceDefinitionPath() string { return service.DefinitionPath() }

func installService(bin, user string) error {
	uid, err := strconv.Atoi(user)
	if err != nil {
		return fail("the service's user must be a uid, not %q", user)
	}
	bootout() // replace an earlier install
	if err := os.WriteFile(service.DefinitionPath(), []byte(servicePlist(bin, uid)), 0o644); err != nil {
		return err
	}
	// A provenance xattr on a file written from a sandboxed or copied context makes
	// launchd refuse the job with "Bootstrap failed: 5: Input/output error".
	_ = exec.Command("xattr", "-c", service.DefinitionPath()).Run()
	return bootstrap()
}

// bootout unloads the service and waits until launchd has really let go of it:
// launchctl returns while the old daemon is still exiting, and a bootstrap in that
// window fails with error 5 (Input/output error).
func bootout() {
	target := "system/" + service.Label
	if exec.Command("launchctl", "print", target).Run() != nil {
		return // not loaded
	}
	_ = exec.Command("launchctl", "bootout", target).Run()
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		if exec.Command("launchctl", "print", target).Run() != nil {
			return
		}
	}
}

// bootstrap loads the service, retrying briefly on error 5, which launchd also returns
// for a moment after an unload.
func bootstrap() error {
	var out []byte
	var err error
	for i := 0; i < 20; i++ {
		out, err = exec.Command("launchctl", "bootstrap", "system", service.DefinitionPath()).CombinedOutput()
		var exit *exec.ExitError
		if err == nil || !errors.As(err, &exit) || exit.ExitCode() != 5 {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if err != nil {
		return fmt.Errorf("launchctl bootstrap: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

func uninstallService() error {
	bootout()
	if err := killswitch.Reset(context.Background()); err != nil {
		return fmt.Errorf("disable Kill Switch: %w", err)
	}
	if err := os.Remove(service.DefinitionPath()); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func restartService() error {
	return sudo("launchctl", "kickstart", "-k", "system/"+service.Label)
}
