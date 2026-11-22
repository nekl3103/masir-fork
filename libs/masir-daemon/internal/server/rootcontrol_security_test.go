//go:build !windows

package server

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/Gozargah/Masir/libs/masir-daemon/internal/identity"
)

func TestRootControlledRejectsMutableLink(t *testing.T) {
	// A trusted target must not hide the caller's control over the link.
	if os.Geteuid() != 0 {
		t.Skip("requires root-owned fixtures outside writable temp ancestry")
	}
	dir, err := os.MkdirTemp("/var", "masir-mutable-link-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	if !rootControlled(dir) {
		t.Fatal("fixture directory must be root-controlled before changing its mode")
	}
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "masir")
	if err := os.Symlink("/bin/sh", link); err != nil {
		t.Fatal(err)
	}
	if rootControlled(link) {
		t.Fatal("accepted a link in a mutable directory")
	}
}

func TestClientBinaryRejectsPrivateFile(t *testing.T) {
	if os.Geteuid() != 0 || !rootControlled(executable()) {
		t.Skip("requires a root-controlled test executable")
	}
	dir, err := os.MkdirTemp("/", "masir-client-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	s := &Server{id: identity.Identity{Privileged: true, UID: 501}}
	path := filepath.Join(dir, "masir")
	if err := os.WriteFile(path, []byte("private data"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := s.clientBinary(path); got != "" {
		t.Fatalf("accepted private file %q", got)
	}
	src, err := os.Open(executable())
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	dst, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		t.Fatal(err)
	}
	if err := dst.Close(); err != nil {
		t.Fatal(err)
	}
	if got := s.clientBinary(path); got != path {
		t.Fatalf("matching trusted binary: got %q, want %q", got, path)
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	var first [1]byte
	if _, err := f.ReadAt(first[:], 0); err != nil {
		f.Close()
		t.Fatal(err)
	}
	first[0] ^= 0xff
	if _, err := f.WriteAt(first[:], 0); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if got := s.clientBinary(path); got != "" {
		t.Fatalf("accepted different binary of same size: %q", got)
	}
}

func TestRootControlledTraversal(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root-owned fixtures outside writable temp ancestry")
	}
	dir, err := os.MkdirTemp("/", "masir-rootcontrol-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	trusted := filepath.Join(dir, "trusted")
	mutable := filepath.Join(dir, "mutable")
	for _, d := range []string{trusted, mutable} {
		if err := os.Mkdir(d, 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(mutable, 0777); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(trusted, "masir")
	if err := os.WriteFile(binary, []byte("binary"), 0755); err != nil {
		t.Fatal(err)
	}
	links := map[string]string{
		"trusted/link":     "masir",
		"mutable/masir":    binary,
		"trusted/indirect": "../mutable/masir",
		"trusted/absolute": binary,
		"trusted/loop":     "loop",
	}
	for name, target := range links {
		if err := os.Symlink(target, filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		path string
		want bool
	}{
		{binary, true}, {filepath.Join(trusted, "link"), true},
		{filepath.Join(trusted, "absolute"), true},
		{filepath.Join(mutable, "masir"), false},
		{filepath.Join(trusted, "indirect"), false},
		{filepath.Join(trusted, "loop"), false},
		{binary + "/../masir", false}, {"relative/masir", false},
	} {
		if got := rootControlled(tc.path); got != tc.want {
			t.Errorf("rootControlled(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
	if err := os.Chmod(binary, 0777); err != nil {
		t.Fatal(err)
	}
	if rootControlled(binary) {
		t.Fatal("accepted writable binary")
	}
}
