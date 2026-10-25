//go:build windows

package artifactstore

import (
	"golang.org/x/sys/windows"
	"os"
	"path/filepath"
)

func checkPath(path string, info os.FileInfo, private bool) error {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	attrs, err := windows.GetFileAttributes(p)
	if err != nil {
		return err
	}
	if attrs&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return fault(Invalid)
	}
	if !info.IsDir() {
		h, err := windows.CreateFile(p, windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
		if err != nil {
			return err
		}
		defer windows.CloseHandle(h)
		var data windows.ByHandleFileInformation
		if err = windows.GetFileInformationByHandle(h, &data); err != nil {
			return err
		}
		if data.NumberOfLinks != 1 {
			return fault(Invalid)
		}
	}
	return nil
}
func checkLocal(path string) error {
	p, err := windows.UTF16PtrFromString(filepath.VolumeName(path) + `\`)
	if err != nil {
		return err
	}
	if windows.GetDriveType(p) != windows.DRIVE_FIXED {
		return fault(Invalid)
	}
	return nil
}
