package artifactstore

import "golang.org/x/sys/unix"

func checkLocal(path string) error {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return err
	}
	// Fail closed: explicitly supported local disk and memory filesystems only.
	switch uint64(st.Type) {
	case 0xef53, 0x58465342, 0x9123683e, 0x01021994, 0x794c7630, 0x2fc12fc1:
		return nil
	}
	return fault(Invalid)
}
