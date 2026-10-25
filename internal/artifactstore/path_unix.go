//go:build linux || darwin

package artifactstore

import (
	"os"
	"syscall"
)

func checkPath(path string, info os.FileInfo, private bool) error {
	if private && info.Mode().Perm()&0077 != 0 {
		return fault(Invalid)
	}
	if !info.IsDir() {
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok || st.Nlink != 1 {
			return fault(Invalid)
		}
	}
	return nil
}
