//go:build !windows

package update

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReplaceFileKeepsBackupPrivate(t *testing.T) {
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "new"), filepath.Join(dir, "masir")
	if err := os.WriteFile(src, []byte("new"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := replaceFile(src, dst); err != nil {
		t.Fatal(err)
	}
	backup, err := os.Stat(dst + ".previous")
	if err != nil {
		t.Fatal(err)
	}
	if mode := backup.Mode().Perm(); mode != 0600 {
		t.Fatalf("backup mode = %04o, want 0600", mode)
	}
	contents, err := os.ReadFile(dst + ".previous")
	if err != nil || string(contents) != "private" {
		t.Fatalf("backup contents = %q, err = %v", contents, err)
	}
}
