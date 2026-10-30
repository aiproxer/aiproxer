//go:build unix

package configsource

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"os"
	"runtime"
	"syscall"
)

// identityFromFileInfo is the portable Unix fallback identity (device + inode)
// used where platform birth-time metadata is unavailable.
func identityFromFileInfo(fi os.FileInfo) (FileIdentity, error) {
	if fi == nil {
		return FileIdentity{}, integrityErr(CategoryUnsupportedType)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || st == nil {
		return FileIdentity{}, fmt.Errorf("configsource: %s", CategoryUnsupportedType)
	}
	var opaque [32]byte
	h := sha256.New()
	_ = binary.Write(h, binary.LittleEndian, uint64(st.Dev))
	_ = binary.Write(h, binary.LittleEndian, uint64(st.Ino))
	copy(opaque[:], h.Sum(nil))
	return FileIdentity{Platform: runtime.GOOS, Scheme: identitySchemeDeviceInode, Opaque: opaque}, nil
}
