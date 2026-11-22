//go:build !windows

package server

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// rootControlled reports whether only root can change path: the file and every
// directory above it belong to root and aren't writable by anyone else. A root daemon
// replaces only such files itself; one in a user's directory could be steered by a link
// the user plants there.
func rootControlled(path string) bool {
	// Require a canonical absolute spelling: the caller uses this same path later.
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return false
	}
	pending := strings.Split(strings.TrimPrefix(path, "/"), "/")
	current := "/"
	links := 0
	for {
		fi, err := os.Lstat(current)
		if err != nil {
			return false
		}
		st, ok := fi.Sys().(*syscall.Stat_t)
		if !ok || st.Uid != 0 {
			return false
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			links++
			if links > 40 {
				return false
			}
			target, err := os.Readlink(current)
			if err != nil {
				return false
			}
			// The link's parent was checked before reaching it. Check the target's
			// entire traversal too, including intermediate links and directories.
			current = filepath.Dir(current)
			if filepath.IsAbs(target) {
				current = "/"
			}
			pending = append(strings.Split(target, "/"), pending...)
			continue
		}
		if fi.Mode().Perm()&0o022 != 0 {
			return false
		}
		if len(pending) == 0 {
			return true
		}
		if !fi.IsDir() {
			return false
		}
		next := pending[0]
		pending = pending[1:]
		current = filepath.Join(current, next)
	}
}
