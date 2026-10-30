//go:build windows

package trust

import (
	"os"

	"golang.org/x/sys/windows"
)

// Win32 GetFinalPathNameByHandle flags (not all exported by x/sys/windows).
const (
	volumeNameDOS = 0x0
	volumeNameNT  = 0x2
)

// confirmOpenedUnderRoot ensures the opened file's canonical final path is
// contained under the trusted root's canonical final path. Comparing two
// handle-derived final paths tolerates subst/mapped drive aliases while still
// rejecting parent-directory junction escapes (final path leaves the root).
func confirmOpenedUnderRoot(root string, f *os.File) error {
	if root == "" || f == nil {
		return ReasonPathEscape
	}
	finalFile, err := finalPathByHandle(windows.Handle(f.Fd()))
	if err != nil {
		return err
	}
	finalRoot, err := finalPathForDirectory(root)
	if err != nil {
		return err
	}
	if !windowsPathContained(finalRoot, finalFile) {
		return ReasonPathEscape
	}
	return nil
}

func finalPathForDirectory(dir string) (string, error) {
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return "", err
	}
	h, err := windows.CreateFile(
		p,
		windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS,
		0,
	)
	if err != nil {
		return "", err
	}
	defer func() { _ = windows.CloseHandle(h) }()
	return finalPathByHandle(h)
}

func finalPathByHandle(h windows.Handle) (string, error) {
	// Prefer NT device paths so subst/mapped drive letters and DOS drive
	// letters collapse to the same volume identity for containment checks.
	if s, err := finalPathByHandleFlags(h, volumeNameNT); err == nil && s != "" {
		return s, nil
	}
	return finalPathByHandleFlags(h, volumeNameDOS)
}

func finalPathByHandleFlags(h windows.Handle, flags uint32) (string, error) {
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n, err := windows.GetFinalPathNameByHandle(h, &buf[0], uint32(len(buf)), flags)
	if err != nil {
		return "", err
	}
	if n == 0 || int(n) > len(buf) {
		return "", ReasonOpenFailed
	}
	return windows.UTF16ToString(buf[:n]), nil
}
