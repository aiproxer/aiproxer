//go:build windows

package configsource

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"os"
	"runtime"

	"golang.org/x/sys/windows"
)

func identityFromFile(f *os.File) (FileIdentity, error) {
	if f == nil {
		return FileIdentity{}, integrityErr(CategoryUnsupportedType)
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(f.Fd()), &info); err != nil {
		return FileIdentity{}, fmt.Errorf("configsource: %s: %w", CategoryPartialUnreadable, err)
	}
	var opaque [32]byte
	h := sha256.New()
	_ = binary.Write(h, binary.LittleEndian, info.VolumeSerialNumber)
	_ = binary.Write(h, binary.LittleEndian, info.FileIndexHigh)
	_ = binary.Write(h, binary.LittleEndian, info.FileIndexLow)
	copy(opaque[:], h.Sum(nil))
	return FileIdentity{Platform: runtime.GOOS, Scheme: identitySchemeFileID, Opaque: opaque}, nil
}

func identityFromPath(path string) (FileIdentity, error) {
	f, err := os.Open(path) // #nosec G304 -- fixed absolute startup path revalidation
	if err != nil {
		if os.IsNotExist(err) {
			return FileIdentity{}, integrityErr(CategoryUnstable)
		}
		return FileIdentity{}, fmt.Errorf("configsource: %s: %w", CategoryPartialUnreadable, err)
	}
	defer func() { _ = f.Close() }()
	return identityFromFile(f)
}

func detectSourceLease(*os.File) (sourceLeaseEvidence, bool) { return sourceLeaseEvidence{}, false }

func captureLeaseMetadata(*os.File) (sourceFileMetadata, error) {
	return sourceFileMetadata{}, integrityErr(CategoryUnsupportedType)
}
