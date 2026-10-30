//go:build linux

package configsource

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const (
	linuxExt4Magic         = 0xef53
	maxMountInfoBytes      = 1 << 20
	linuxLeaseStatxMask    = unix.STATX_BASIC_STATS | unix.STATX_MNT_ID
	linuxLeaseRequiredMask = unix.STATX_INO | unix.STATX_SIZE | unix.STATX_MTIME | unix.STATX_CTIME | unix.STATX_MNT_ID
)

type linuxLeaseDetectionOps struct {
	statx         func(*os.File, int, *unix.Statx_t) error
	fstatfs       func(*os.File, *unix.Statfs_t) error
	readMountInfo func() ([]byte, error)
}

func defaultLinuxLeaseDetectionOps() linuxLeaseDetectionOps {
	return linuxLeaseDetectionOps{
		statx: func(f *os.File, mask int, stx *unix.Statx_t) error {
			return unix.Statx(int(f.Fd()), "", unix.AT_EMPTY_PATH, mask, stx)
		},
		fstatfs: func(f *os.File, fs *unix.Statfs_t) error {
			return unix.Fstatfs(int(f.Fd()), fs)
		},
		readMountInfo: readSelfMountInfo,
	}
}

func statxHandle(f *os.File, mask uint32) (unix.Statx_t, error) {
	if f == nil {
		return unix.Statx_t{}, fmt.Errorf("configsource: nil file")
	}
	var stx unix.Statx_t
	err := unix.Statx(int(f.Fd()), "", unix.AT_EMPTY_PATH, int(mask), &stx)
	return stx, err
}

func detectLinuxExt4Lease(f *os.File) (sourceLeaseEvidence, bool) {
	return detectLinuxExt4LeaseWithOps(f, defaultLinuxLeaseDetectionOps())
}

func detectSourceLease(f *os.File) (sourceLeaseEvidence, bool) {
	return detectLinuxExt4Lease(f)
}

func captureLeaseMetadata(f *os.File) (sourceFileMetadata, error) {
	stx, err := statxHandle(f, linuxLeaseStatxMask)
	if err != nil || stx.Mask&uint32(linuxLeaseRequiredMask) != uint32(linuxLeaseRequiredMask) {
		return sourceFileMetadata{}, leaseIntegrityErr("lease_unavailable")
	}
	return sourceFileMetadata{
		identity: identityFromStatx(&stx), size: int64(stx.Size),
		modTime:    unixTime(stx.Mtime.Sec, stx.Mtime.Nsec),
		changeTime: unixTime(stx.Ctime.Sec, stx.Ctime.Nsec), hasChangeTime: true,
		mountID: stx.Mnt_id, devMajor: stx.Dev_major, devMinor: stx.Dev_minor,
	}, nil
}

func unixTime(sec int64, nsec uint32) time.Time {
	return time.Unix(sec, int64(nsec)).UTC()
}

func detectLinuxExt4LeaseWithOps(f *os.File, ops linuxLeaseDetectionOps) (sourceLeaseEvidence, bool) {
	if f == nil || ops.statx == nil || ops.fstatfs == nil || ops.readMountInfo == nil {
		return sourceLeaseEvidence{}, false
	}
	var stx unix.Statx_t
	if err := ops.statx(f, linuxLeaseStatxMask, &stx); err != nil ||
		stx.Mask&uint32(unix.STATX_INO|unix.STATX_MNT_ID) != uint32(unix.STATX_INO|unix.STATX_MNT_ID) {
		return sourceLeaseEvidence{}, false
	}
	var fs unix.Statfs_t
	if err := ops.fstatfs(f, &fs); err != nil || uint64(fs.Type) != linuxExt4Magic {
		return sourceLeaseEvidence{}, false
	}
	mountInfo, err := ops.readMountInfo()
	if err != nil || len(mountInfo) > maxMountInfoBytes {
		return sourceLeaseEvidence{}, false
	}
	matched, err := parseExt4MountInfo(mountInfo, stx.Mnt_id, stx.Dev_major, stx.Dev_minor)
	if err != nil || !matched {
		return sourceLeaseEvidence{}, false
	}
	return sourceLeaseEvidence{
		identity: identityFromStatx(&stx), mountID: stx.Mnt_id,
		devMajor: stx.Dev_major, devMinor: stx.Dev_minor,
	}, true
}

func readSelfMountInfo() ([]byte, error) {
	f, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxMountInfoBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxMountInfoBytes {
		return nil, fmt.Errorf("configsource: mountinfo exceeds bound")
	}
	return data, nil
}

func parseExt4MountInfo(data []byte, mountID uint64, devMajor, devMinor uint32) (bool, error) {
	if len(data) == 0 || len(data) > maxMountInfoBytes || data[len(data)-1] != '\n' {
		return false, fmt.Errorf("configsource: invalid mountinfo size")
	}
	matched := 0
	matchedType := ""
	targetSeen := false
	for line := range strings.SplitSeq(string(data), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			return false, fmt.Errorf("configsource: malformed mountinfo")
		}
		// Identify the record before validating it. Unrelated namespace mounts
		// legitimately encode a non-path root such as `net:[4026532637]`, which
		// is not target evidence and must not invalidate the target record.
		lineMountID, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			return false, fmt.Errorf("configsource: malformed mount id")
		}
		if lineMountID != mountID {
			continue
		}
		if targetSeen {
			return false, fmt.Errorf("configsource: duplicate mount id")
		}
		targetSeen = true
		fsType, err := parseMountInfoTarget(fields)
		if err != nil {
			return false, err
		}
		major, minor, err := parseMountDevice(fields[2])
		if err != nil {
			return false, err
		}
		if major != devMajor || minor != devMinor {
			return false, nil
		}
		matched++
		matchedType = fsType
	}
	if matched != 1 || matchedType != "ext4" {
		return false, nil
	}
	return true, nil
}

// parseMountInfoTarget validates the full mountinfo structure of the record
// whose mount ID equals the certified target and returns its filesystem type.
// Structural, parent-ID, and root/mountpoint encoding rules apply only to target
// evidence, so a single exact target record remains the whole contract.
func parseMountInfoTarget(fields []string) (string, error) {
	if len(fields) < 10 {
		return "", fmt.Errorf("configsource: malformed mountinfo")
	}
	separator := -1
	for i := 6; i < len(fields); i++ {
		if fields[i] == "-" {
			if separator >= 0 {
				return "", fmt.Errorf("configsource: ambiguous mountinfo separator")
			}
			separator = i
		}
	}
	if separator < 6 || separator+4 != len(fields) {
		return "", fmt.Errorf("configsource: malformed mountinfo fields")
	}
	if _, err := strconv.ParseUint(fields[1], 10, 64); err != nil {
		return "", fmt.Errorf("configsource: malformed parent mount id")
	}
	if !validMountPath(fields[3]) || !validMountPath(fields[4]) {
		return "", fmt.Errorf("configsource: malformed mount path")
	}
	return fields[separator+1], nil
}

func validMountPath(path string) bool {
	if !strings.HasPrefix(path, "/") {
		return false
	}
	for i := 0; i < len(path); i++ {
		if path[i] != '\\' {
			continue
		}
		if i+3 >= len(path) {
			return false
		}
		switch path[i+1 : i+4] {
		case "040", "011", "012", "134":
			i += 3
		default:
			return false
		}
	}
	return true
}

func parseMountDevice(value string) (uint32, uint32, error) {
	majorText, minorText, ok := strings.Cut(value, ":")
	if !ok || strings.Contains(minorText, ":") {
		return 0, 0, fmt.Errorf("configsource: malformed mount device")
	}
	major, err := strconv.ParseUint(majorText, 10, 32)
	if err != nil {
		return 0, 0, fmt.Errorf("configsource: malformed mount major")
	}
	minor, err := strconv.ParseUint(minorText, 10, 32)
	if err != nil {
		return 0, 0, fmt.Errorf("configsource: malformed mount minor")
	}
	return uint32(major), uint32(minor), nil
}

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
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return FileIdentity{}, integrityErr(CategoryUnstable)
		}
		return FileIdentity{}, fmt.Errorf("configsource: %s: %w", CategoryPartialUnreadable, err)
	}
	defer func() { _ = f.Close() }()
	return identityFromFile(f)
}

func identityFromStatx(stx *unix.Statx_t) FileIdentity {
	return leaseIdentityFromStatx(stx)
}

func leaseIdentityFromStatx(stx *unix.Statx_t) FileIdentity {
	var opaque [32]byte
	h := sha256.New()
	_ = binary.Write(h, binary.LittleEndian, uint64(stx.Dev_major))
	_ = binary.Write(h, binary.LittleEndian, uint64(stx.Dev_minor))
	_ = binary.Write(h, binary.LittleEndian, stx.Ino)
	copy(opaque[:], h.Sum(nil))
	return FileIdentity{Platform: runtime.GOOS, Scheme: identitySchemeLinuxExt4Lease, Opaque: opaque}
}
