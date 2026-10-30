//go:build windows

package trust

import "golang.org/x/sys/windows"

// FileIdentity is the stable NTFS/ReFS identity of an open file.
type FileIdentity struct {
	VolumeSerialNumber uint32
	FileIndexHigh      uint32
	FileIndexLow       uint32
}

func fileIdentity(h windows.Handle) (FileIdentity, error) {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		return FileIdentity{}, err
	}
	return FileIdentity{
		VolumeSerialNumber: info.VolumeSerialNumber,
		FileIndexHigh:      info.FileIndexHigh,
		FileIndexLow:       info.FileIndexLow,
	}, nil
}

// FileIdentityFromHandle exports identity for process-host launch checks.
func FileIdentityFromHandle(h windows.Handle) (FileIdentity, error) {
	return fileIdentity(h)
}

// FileIdentityFromFile reads identity from an *os.File handle.
func FileIdentityFromOSFile(fd uintptr) (FileIdentity, error) {
	return fileIdentity(windows.Handle(fd))
}
