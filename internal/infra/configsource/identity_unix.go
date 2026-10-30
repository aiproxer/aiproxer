//go:build unix && !linux

package configsource

import (
	"fmt"
	"os"
)

func identityFromFile(f *os.File) (FileIdentity, error) {
	if f == nil {
		return FileIdentity{}, integrityErr(CategoryUnsupportedType)
	}
	fi, err := f.Stat()
	if err != nil {
		return FileIdentity{}, fmt.Errorf("configsource: %s: %w", CategoryPartialUnreadable, err)
	}
	return identityFromFileInfo(fi)
}

func identityFromPath(path string) (FileIdentity, error) {
	fi, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return FileIdentity{}, integrityErr(CategoryUnstable)
		}
		return FileIdentity{}, fmt.Errorf("configsource: %s: %w", CategoryPartialUnreadable, err)
	}
	return identityFromFileInfo(fi)
}

func detectSourceLease(*os.File) (sourceLeaseEvidence, bool) { return sourceLeaseEvidence{}, false }

func captureLeaseMetadata(*os.File) (sourceFileMetadata, error) {
	return sourceFileMetadata{}, integrityErr(CategoryUnsupportedType)
}
