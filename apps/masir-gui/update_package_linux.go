//go:build linux

package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/egoist/mygo"
	"golang.org/x/mod/semver"
	"golang.org/x/sys/unix"
)

// platformInstallBlock says whether a package manager owns this app, or a folder the
// person can't write keeps MyGo's updater out. A development build is never blocked.
func platformInstallBlock() installBlock {
	if mygo.IsDev() || !semver.IsValid("v"+strings.TrimPrefix(mygo.App.Version(), "v")) {
		return blockNone
	}
	exe, err := os.Executable()
	if err != nil {
		return blockNone
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	return installBlockOf(exe)
}

func installBlockOf(exe string) installBlock {
	// "masir-desktop: /path", or "masir-desktop:amd64: /path" on a multiarch system.
	if out, err := exec.Command("dpkg-query", "-S", exe).Output(); err == nil && strings.HasPrefix(string(out), "masir-desktop:") {
		return blockPackageDeb
	}
	if out, err := exec.Command("rpm", "-qf", "--queryformat", "%{NAME}", exe).Output(); err == nil && strings.TrimSpace(string(out)) == "masir-desktop" {
		return blockPackageRpm
	}
	if unix.Access(filepath.Dir(exe), unix.W_OK) != nil {
		return blockReadOnly
	}
	return blockNone
}

// platformHelperTrusted is the check before pkexec runs the bundled CLI as root: only a
// file root alone can change, through directories root alone can change, is run. The
// resolved path is returned, so pkexec runs exactly the file that was checked.
func platformHelperTrusted() (string, error) {
	bin := bundledBinary()
	if bin == "" {
		return "", errors.New(tr("this Masir has no bundled masir to install the update"))
	}
	real, err := filepath.EvalSymlinks(bin)
	if err != nil {
		return "", err
	}
	if err := rootOwnedPath(real, os.Lstat); err != nil {
		return "", err
	}
	return real, nil
}

// rootOwnedPath checks path and every directory above it: owned by root, not writable
// by group or others, and not a symlink.
func rootOwnedPath(path string, lstat func(string) (os.FileInfo, error)) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf(tr("%s isn't an absolute path"), path)
	}
	for p := filepath.Clean(path); ; p = filepath.Dir(p) {
		fi, err := lstat(p)
		if err != nil {
			return err
		}
		st, ok := fi.Sys().(*syscall.Stat_t)
		switch {
		case fi.Mode()&os.ModeSymlink != 0:
			return fmt.Errorf(tr("%s is a symlink; reinstall Masir"), p)
		case !ok || st.Uid != 0:
			return fmt.Errorf(tr("%s isn't owned by root; reinstall Masir"), p)
		case fi.Mode().Perm()&0o022 != 0:
			return fmt.Errorf(tr("%s can be changed by other accounts; reinstall Masir"), p)
		}
		if filepath.Dir(p) == p {
			return nil
		}
	}
}

// platformFetchPackage downloads and verifies the package as the person, through the
// bundled CLI.
func platformFetchPackage(ctx context.Context, r packageFetch, progress func(done, total int64)) (packageFiles, error) {
	bin := bundledBinary()
	if bin == "" {
		return packageFiles{}, errors.New(tr("this Masir has no bundled masir to download the update"))
	}
	return runFetch(ctx, bin, r, progress)
}

// runFetch runs `masir desktop fetch-package`. A non-zero exit becomes an elevationError
// carrying the exit code, the one shape packageFailure reads.
func runFetch(ctx context.Context, bin string, r packageFetch, progress func(done, total int64)) (packageFiles, error) {
	args := []string{"desktop", "fetch-package", "--version", r.version, "--format", r.format, "--dir", r.dir}
	if r.proxy != "" {
		args = append(args, "--proxy", r.proxy)
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = append(os.Environ(), "NO_COLOR=1", "TERM=dumb")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return packageFiles{}, err
	}
	if err := cmd.Start(); err != nil {
		return packageFiles{}, err
	}
	files, perr := parseFetchOutput(out, progress)
	if err := cmd.Wait(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() > 0 {
			return packageFiles{}, &elevationError{kind: elevationFailed, code: ee.ExitCode(), msg: childError(stderr.String(), err)}
		}
		return packageFiles{}, err
	}
	return files, perr
}

// childError is the CLI's stderr without its "error: " prefixes, for a tooltip.
func childError(stderr string, err error) string {
	var lines []string
	for _, l := range strings.Split(strings.TrimSpace(stderr), "\n") {
		if l = strings.TrimSpace(strings.TrimPrefix(l, "error: ")); l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) == 0 {
		return err.Error()
	}
	return strings.Join(lines, "\n")
}

// parseFetchOutput reads the fetch protocol to the end: `progress <done> <total>` lines,
// then the package, checksum and signature paths. Any other line is ignored.
func parseFetchOutput(r io.Reader, progress func(done, total int64)) (packageFiles, error) {
	var f packageFiles
	br := bufio.NewReader(r)
	for {
		line, rerr := br.ReadString('\n')
		key, val, _ := strings.Cut(strings.TrimRight(line, "\r\n"), " ")
		switch key {
		case "progress":
			d, t, _ := strings.Cut(val, " ")
			done, err1 := strconv.ParseInt(d, 10, 64)
			total, err2 := strconv.ParseInt(t, 10, 64)
			if err1 == nil && err2 == nil {
				progress(done, total)
			}
		case "package":
			f.pkg = val
		case "checksums":
			f.sums = val
		case "signature":
			f.sig = val
		}
		if rerr != nil {
			break
		}
	}
	if f.pkg == "" || f.sums == "" || f.sig == "" {
		return packageFiles{}, errors.New(tr("the download ended without a verified package"))
	}
	return f, nil
}

// platformInstallPackage installs the verified package as root, with one password.
func platformInstallPackage(bin string, f packageFiles, uid int, authorized func()) error {
	_, err := pkexecAnnounced(authorized, bin, "desktop", "install-package",
		"--package", f.pkg, "--checksums", f.sums, "--signature", f.sig, "--user", strconv.Itoa(uid))
	return err
}
