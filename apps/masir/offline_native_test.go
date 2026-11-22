package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/Gozargah/Masir/libs/masir-core/fileops"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"aead.dev/minisign"
	"github.com/Gozargah/Masir/libs/masir-core/client"
	"github.com/Gozargah/Masir/libs/masir-core/paths"
	"github.com/Gozargah/Masir/libs/masir-core/service"
	"github.com/Gozargah/Masir/libs/masir-core/updatebundle"
	masird "github.com/Gozargah/Masir/libs/masir-daemon"
	pb "github.com/Gozargah/Masir/libs/masir-proto/gen/masir/daemon/v1"
)

const nativeOldVersion = "0.1.0"

// This public key verifies the fixed v0.1.0 fixture, including in fork CI.
const nativeOldPublisherKey = "RWTZsqFUrG9b8HKgjHcKrTYW7ISgHH8oJT5NeKxSunKPaiRD8M/gT1rI"
const nativeNewVersion = "0.2.0"
const nativeUpdatePackage = "github.com/Gozargah/Masir/libs/masir-daemon/internal/update.publicKeys"

// This opt-in installs a real service only on fresh, disposable CI runners. It
// never runs as part of the ordinary developer test loop or on an existing install.
func TestOfflineNativeServiceLifecycle(t *testing.T) {
	if os.Getenv("MASIR_OFFLINE_NATIVE") != "1" || os.Getenv("GITHUB_ACTIONS") != "true" {
		t.Skip("disposable native CI only")
	}
	if runtime.GOOS != "darwin" && runtime.GOOS != "windows" {
		t.Skip("Linux lifecycle already covered by the Lima fixture")
	}
	if service.Installed() {
		t.Fatal("refusing to replace a pre-existing service")
	}
	if runtime.GOOS == "darwin" {
		if _, err := os.Lstat(service.LegacyDarwinDir); !os.IsNotExist(err) {
			t.Fatal("refusing to touch an existing legacy service directory")
		}
	}
	if _, err := os.Stat(service.InstallDir()); !os.IsNotExist(err) {
		t.Fatal("refusing to touch an existing service directory")
	}
	if runtime.GOOS == "windows" && !elevated() {
		t.Fatal("native Windows runner must have an elevated token")
	}
	u, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	config := paths.ConfigDirOf(u.HomeDir)
	if _, err := os.Stat(config); !os.IsNotExist(err) {
		t.Fatal("refusing to modify an existing user profile")
	}
	pub, key, err := minisign.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pubText, err := pub.MarshalText()
	if err != nil {
		t.Fatal(err)
	}
	public := strings.TrimSpace(strings.Split(string(pubText), "\n")[1])
	root := t.TempDir()
	oldDir := filepath.Join(root, "old")
	payload := filepath.Join(root, "Desktop components 'with spaces'")
	for _, dir := range []string{oldDir, payload} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	nativeReleaseFixture(t, oldDir, nativeOldVersion)
	for _, name := range []string{"geoip.dat", "geosite.dat"} {
		b, e := os.ReadFile(filepath.Join(oldDir, name))
		if e != nil {
			t.Fatal(e)
		}
		if e := os.WriteFile(filepath.Join(payload, name), b, 0644); e != nil {
			t.Fatal(e)
		}
	}
	name := updatebundle.Names(runtime.GOOS)[0]
	old := filepath.Join(oldDir, name)
	helper := filepath.Join(payload, name)
	child := filepath.Join(root, "native-child.test")
	if runtime.GOOS == "windows" {
		child += ".exe"
	}
	run := func(program string, args ...string) []byte {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		out, e := exec.CommandContext(ctx, program, args...).CombinedOutput()
		if e != nil {
			t.Fatalf("%s failed: %v\n%s", filepath.Base(program), e, out)
		}
		return out
	}
	run("go", "build", "-o", helper, "-ldflags", "-X main.version="+nativeNewVersion+" -X "+nativeUpdatePackage+"="+public, ".")
	run("go", "test", "-c", "-o", child, "-ldflags", "-X "+nativeUpdatePackage+"="+public, ".")
	data, err := updatebundle.Create(payload, nativeNewVersion, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(payload, updatebundle.DescriptorName), data, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(payload, updatebundle.SignatureName), minisign.Sign(key, data), 0644); err != nil {
		t.Fatal(err)
	}
	privileged := func(program string, args ...string) []byte {
		t.Helper()
		if runtime.GOOS == "darwin" {
			return run("sudo", append([]string{"-n", program}, args...)...)
		}
		return run(program, args...)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		command := []string{helper, "daemon", "disable"}
		if runtime.GOOS == "darwin" {
			command = append([]string{"sudo", "-n"}, command...)
		}
		if out, e := exec.CommandContext(ctx, command[0], command[1:]...).CombinedOutput(); e != nil {
			t.Errorf("service cleanup: %v %s", e, out)
		}
		if runtime.GOOS == "darwin" {
			if out, e := exec.Command("sudo", "-n", "rm", "-rf", service.InstallDir(), service.LegacyDarwinDir).CombinedOutput(); e != nil {
				t.Errorf("fixture directory cleanup: %v %s", e, out)
			}
		} else {
			deadline := time.Now().Add(5 * time.Second)
			var removeErr error
			for {
				removeErr = os.RemoveAll(service.InstallDir())
				if removeErr == nil || time.Now().After(deadline) {
					break
				}
				time.Sleep(100 * time.Millisecond)
			}
			if removeErr != nil {
				t.Errorf("fixture directory cleanup: %v", removeErr)
			}
		}
		if e := os.RemoveAll(config); e != nil {
			t.Errorf("profile cleanup: %v", e)
		}
	})
	// A desktop launch creates the user's profile before administrator install.
	// The old CLI can otherwise create it as root on a fresh macOS runner, which
	// its daemon correctly refuses to serve. Model the real desktop install order.
	if err := os.MkdirAll(config, 0700); err != nil {
		t.Fatal(err)
	}
	privileged(old, "daemon", "enable", "--user", u.Uid)
	nativeWaitVersion(t, nativeOldVersion)
	oldBytes, err := os.ReadFile(old)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct{ name, fault string }{
		{"stop-and-resume-errors", "stop-and-resume-errors"},
		{"saved-state-after-stop", "saved-state-after-stop"},
		{"version-race-after-stop", "version-race-after-stop"},
		{"crash-lock", "crash-lock"},
		// Deliberately repeat an import after the crash: this proves OS lock release.
		{"lock-reacquired-after-crash", "stop-and-resume-errors"},
	}
	if runtime.GOOS == "darwin" {
		cases = append([]struct{ name, fault string }{{"legacy-unsupported", "legacy-unsupported"}}, cases...)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			which := tc.fault
			args := []string{child, "-test.run=^TestOfflineNativeChild$", "-test.v"}
			env := []string{"GITHUB_ACTIONS=true", "MASIR_NATIVE_CHILD=" + which, "MASIR_NATIVE_PAYLOAD=" + payload, "MASIR_NATIVE_USER=" + u.Uid}
			var cmd *exec.Cmd
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			if runtime.GOOS == "darwin" {
				cmd = exec.CommandContext(ctx, "sudo", append(append([]string{"-n", "env"}, env...), args...)...)
			} else {
				cmd = exec.CommandContext(ctx, args[0], args[1:]...)
				cmd.Env = append(os.Environ(), env...)
			}
			out, e := cmd.CombinedOutput()
			if which == "crash-lock" {
				var exit *exec.ExitError
				if !errors.As(e, &exit) || exit.ExitCode() != 86 {
					t.Fatalf("crash probe: %v %s", e, out)
				}
			} else if e != nil {
				t.Fatalf("native fault probe: %v\n%s", e, out)
			}
			installedPath := service.BinaryPath()
			now, e := os.ReadFile(installedPath)
			if e != nil || !bytes.Equal(now, oldBytes) {
				t.Fatalf("failed update changed installed bytes: %v", e)
			}
			nativeWaitVersion(t, nativeOldVersion)
		})
	}
	// The final successful import exercises this OS's actual supervisor, not a mock.
	privileged(helper, "daemon", "import-desktop", "--from", payload, "--user", u.Uid)
	nativeWaitVersion(t, nativeNewVersion)
	nativeAssertProtectedInstallation(t)
	previous, err := os.ReadFile(service.BinaryPath() + ".previous")
	if runtime.GOOS != "windows" {
		previous, err = exec.Command("sudo", "-n", "cat", service.BinaryPath()+".previous").Output()
	}
	if err != nil || sha256.Sum256(previous) != sha256.Sum256(oldBytes) {
		t.Fatalf("rollback binary not preserved: %v", err)
	}
}

func nativeWaitVersion(t *testing.T, version string) {
	t.Helper()
	conn, c, err := client.Dial()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var last string
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		r, e := c.GetStatus(ctx, &pb.GetStatusRequest{})
		cancel()
		if e != nil {
			last = e.Error()
		} else {
			last = fmt.Sprintf("status: %v", r.GetStatus())
		}
		if e == nil && r.GetStatus().GetVersion() == version {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	if runtime.GOOS == "darwin" {
		for _, args := range [][]string{{"-n", "tail", "-80", "/var/log/masird.log"}, {"-n", "launchctl", "print", "system/" + service.Label}} {
			out, _ := exec.Command("sudo", args...).CombinedOutput()
			t.Logf("diagnostic: %s", out)
		}
	}
	t.Fatalf("supervised daemon did not serve version %s: %s", version, last)
}

// The child embeds the ephemeral fixture's public key at build time and runs with
// administrator rights. Its callbacks inject faults at real stop/recovery boundaries.
func TestOfflineNativeChild(t *testing.T) {
	which := os.Getenv("MASIR_NATIVE_CHILD")
	if which == "" || os.Getenv("GITHUB_ACTIONS") != "true" {
		t.Skip("native fixture child only")
	}
	if !elevated() {
		t.Fatal("fault probe needs an elevated disposable runner")
	}
	payload, who := os.Getenv("MASIR_NATIVE_PAYLOAD"), os.Getenv("MASIR_NATIVE_USER")
	if which == "legacy-unsupported" {
		nativeRejectUnsupportedLegacy(t, payload, who)
		return
	}
	u, err := user.LookupId(who)
	if err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(paths.ConfigDirOf(u.HomeDir), "routing.json")
	saved, readErr := os.ReadFile(config)
	if readErr != nil && !os.IsNotExist(readErr) {
		t.Fatal(readErr)
	}
	// Before the first migration there is deliberately no executable at the new
	// path. Compare recovery with the currently active legacy bytes; publication
	// must create the protected destination before the resume callback runs.
	baseline := service.BinaryPath()
	if service.LegacyLocation() {
		baseline = service.LegacyDarwinBinary
	}
	original, err := os.ReadFile(baseline)
	if err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(payload, updatebundle.Names(runtime.GOOS)[0])
	stopErr, resumeErr := errors.New("injected stop result"), errors.New("injected resume result")
	resumed := false
	replaced := false
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	err = masird.InstallDesktopOffline(ctx, payload, who, nativeNewVersion, func() error {
		if which == "crash-lock" {
			os.Exit(86)
		}
		if e := stopOfflineService(); e != nil {
			return e
		}
		switch which {
		case "stop-and-resume-errors":
			return stopErr
		case "saved-state-after-stop":
			return os.WriteFile(config, []byte(`{"rules":[{"tag":"fault","match":{"port":"not-a-port"},"target":{"kind":"direct"}}]}`), 0600)
		case "version-race-after-stop":
			incoming, e := os.ReadFile(helper)
			if e != nil {
				return e
			}
			if e := fileops.Rename(service.BinaryPath(), service.BinaryPath()+".native-before"); e != nil {
				return e
			}
			replaced = true
			return os.WriteFile(service.BinaryPath(), incoming, 0755)
		default:
			return fmt.Errorf("unknown fault case %s", which)
		}
	}, func() error {
		resumed = true
		// A competing helper must still be excluded throughout recovery.
		competing, e := exec.CommandContext(ctx, helper, "daemon", "import-desktop", "--from", payload, "--user", who).CombinedOutput()
		if e == nil || !strings.Contains(string(competing), "another offline service update") {
			t.Errorf("lock not held through recovery: %v %s", e, competing)
		}
		if readErr == nil {
			if e := os.WriteFile(config, saved, 0600); e != nil {
				return e
			}
		} else {
			if e := os.Remove(config); e != nil && !os.IsNotExist(e) {
				return e
			}
		}
		if replaced {
			if e := os.Remove(service.BinaryPath()); e != nil {
				return e
			}
			if e := fileops.Rename(service.BinaryPath()+".native-before", service.BinaryPath()); e != nil {
				return e
			}
		}
		e = startOfflineService()
		if which == "stop-and-resume-errors" {
			return errors.Join(e, resumeErr)
		}
		return e
	})
	if !resumed {
		t.Fatalf("failure did not attempt supervisor recovery: %v", err)
	}
	if err == nil {
		t.Fatal("fault was accepted")
	}
	switch which {
	case "stop-and-resume-errors":
		if !errors.Is(err, stopErr) || !errors.Is(err, resumeErr) {
			t.Fatalf("lost stop or recovery error: %v", err)
		}
	case "saved-state-after-stop":
		if !strings.Contains(err.Error(), "can't build") {
			t.Fatalf("saved-state recheck did not reject edited rules: %v", err)
		}
	case "version-race-after-stop":
		if !strings.Contains(err.Error(), "service changed") {
			t.Fatalf("version race was not rejected: %v", err)
		}
	}
	installed, e := os.ReadFile(service.BinaryPath())
	if e != nil || !bytes.Equal(installed, original) {
		t.Fatalf("failed import did not retain original binary: %v", e)
	}
}

// Download/authenticate the historical release only during CI fixture setup. The
// actual import gets no network client and uses solely the local signed payload.
func nativeReleaseFixture(t *testing.T, dest, releaseVersion string) {
	t.Helper()
	ext := ".tar.gz"
	if runtime.GOOS == "windows" {
		ext = ".zip"
	}
	asset := fmt.Sprintf("masir-cli-%s-%s-%s%s", runtime.GOOS, releaseVersion, runtime.GOARCH, ext)
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	out, e := exec.CommandContext(ctx, "gh", "release", "download", "v"+releaseVersion, "--repo", "Gozargah/Masir", "--dir", dir, "--pattern", asset, "--pattern", "checksums.txt", "--pattern", "checksums.txt.minisig").CombinedOutput()
	if e != nil {
		t.Fatalf("historical release fixture: %v %s", e, out)
	}
	read := func(name string) []byte {
		b, e := os.ReadFile(filepath.Join(dir, name))
		if e != nil {
			t.Fatal(e)
		}
		return b
	}
	list, sig := read("checksums.txt"), read("checksums.txt.minisig")
	var publisher minisign.PublicKey
	if err := publisher.UnmarshalText([]byte(nativeOldPublisherKey)); err != nil {
		t.Fatalf("invalid historical fixture public key: %v", err)
	}
	if !minisign.Verify(publisher, list, sig) {
		t.Fatal("historical fixture publisher signature is invalid")
	}
	archive := read(asset)
	sum := sha256.Sum256(archive)
	want := ""
	for _, line := range strings.Split(string(list), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == asset {
			if want != "" {
				t.Fatal("duplicate checksum")
			}
			want = f[0]
		}
	}
	if want != hex.EncodeToString(sum[:]) {
		t.Fatal("historical fixture archive hash mismatch")
	}
	found := map[string]bool{}
	extract := func(name string, r io.Reader) {
		name = filepath.Base(name)
		if name != updatebundle.Names(runtime.GOOS)[0] && name != "geoip.dat" && name != "geosite.dat" {
			return
		}
		if found[name] {
			t.Fatal("duplicate fixture member")
		}
		found[name] = true
		f, e := os.OpenFile(filepath.Join(dest, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0755)
		if e != nil {
			t.Fatal(e)
		}
		n, e := io.Copy(f, io.LimitReader(r, (256<<20)+1))
		e = errors.Join(e, f.Close())
		if e != nil || n == 0 || n > 256<<20 {
			t.Fatalf("invalid fixture member: %v", e)
		}
	}
	if ext == ".zip" {
		z, e := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
		if e != nil {
			t.Fatal(e)
		}
		for _, entry := range z.File {
			if !entry.Mode().IsRegular() {
				continue
			}
			r, e := entry.Open()
			if e != nil {
				t.Fatal(e)
			}
			extract(entry.Name, r)
			r.Close()
		}
	} else {
		z, e := gzip.NewReader(bytes.NewReader(archive))
		if e != nil {
			t.Fatal(e)
		}
		defer z.Close()
		tr := tar.NewReader(z)
		for {
			h, e := tr.Next()
			if e == io.EOF {
				break
			}
			if e != nil {
				t.Fatal(e)
			}
			if h.Typeflag == tar.TypeReg {
				extract(h.Name, tr)
			}
		}
	}
	if len(found) != 3 {
		t.Fatalf("historical fixture missing members: %v", found)
	}
}
