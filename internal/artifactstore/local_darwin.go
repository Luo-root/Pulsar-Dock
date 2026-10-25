package artifactstore

import "golang.org/x/sys/unix"

func checkLocal(path string) error {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return err
	}
	if st.Flags&unix.MNT_LOCAL == 0 {
		return fault(Invalid)
	}
	return nil
}
