//go:build linux

package configsource

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// TestStatxIdentity_LeaseIgnoresBirthTime proves the ext4 lease identity uses
// device and inode while a live descriptor, rather than birth time, supplies
// the inode-lifetime guarantee.
func TestStatxIdentity_LeaseIgnoresBirthTime(t *testing.T) {
	t.Parallel()
	base := unix.Statx_t{Dev_major: 1, Dev_minor: 2, Ino: 100, Btime: unix.StatxTimestamp{Sec: 10, Nsec: 1}}
	same := base
	differentBirthTime := base
	differentBirthTime.Btime = unix.StatxTimestamp{Sec: 10, Nsec: 2}

	idBase := identityFromStatx(&base)
	idSame := identityFromStatx(&same)
	idDifferentBirth := identityFromStatx(&differentBirthTime)

	if idBase != idSame {
		t.Fatal("identical statx fields must produce identical identity")
	}
	if idBase != idDifferentBirth {
		t.Fatal("birth time must not participate in the pinned ext4 identity")
	}
	if idBase.Scheme != "linux-ext4-dev-ino-lease-v1" {
		t.Fatalf("statx identity scheme=%q want linux-ext4-dev-ino-lease-v1", idBase.Scheme)
	}
}

func TestDetectLinuxExt4LeaseRequiresConcordantHandleEvidence(t *testing.T) {
	t.Parallel()
	file, err := os.Create(filepath.Join(t.TempDir(), "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()

	validMount := "43 1 8:1 / /tmp rw,relatime - ext4 /dev/loop0 rw\n"
	base := linuxLeaseDetectionOps{
		statx: func(_ *os.File, _ int, stx *unix.Statx_t) error {
			*stx = unix.Statx_t{
				Mask: unix.STATX_INO | unix.STATX_MNT_ID,
				Ino:  7, Mnt_id: 43, Dev_major: 8, Dev_minor: 1,
			}
			return nil
		},
		fstatfs: func(_ *os.File, fs *unix.Statfs_t) error {
			fs.Type = linuxExt4Magic
			return nil
		},
		readMountInfo: func() ([]byte, error) { return []byte(validMount), nil },
	}
	if evidence, ok := detectLinuxExt4LeaseWithOps(file, base); !ok || evidence.identity.Scheme != identitySchemeLinuxExt4Lease {
		t.Fatalf("valid ext4 handle evidence rejected: evidence=%+v ok=%v", evidence, ok)
	}

	cases := []struct {
		name string
		edit func(*linuxLeaseDetectionOps)
	}{
		{name: "nonnumeric parent", edit: func(ops *linuxLeaseDetectionOps) {
			ops.readMountInfo = func() ([]byte, error) { return []byte("43 malformed 8:1 / /tmp rw - ext4 /dev/loop0 rw\n"), nil }
		}},
		{name: "missing parent", edit: func(ops *linuxLeaseDetectionOps) {
			ops.readMountInfo = func() ([]byte, error) { return []byte("43 8:1 / /tmp rw - ext4 /dev/loop0 rw\n"), nil }
		}},
		{name: "relative root", edit: func(ops *linuxLeaseDetectionOps) {
			ops.readMountInfo = func() ([]byte, error) { return []byte("43 1 8:1 root /tmp rw - ext4 /dev/loop0 rw\n"), nil }
		}},
		{name: "relative mountpoint", edit: func(ops *linuxLeaseDetectionOps) {
			ops.readMountInfo = func() ([]byte, error) { return []byte("43 1 8:1 / tmp rw - ext4 /dev/loop0 rw\n"), nil }
		}},
		{name: "extra post separator field", edit: func(ops *linuxLeaseDetectionOps) {
			ops.readMountInfo = func() ([]byte, error) { return []byte("43 1 8:1 / /tmp rw - ext4 /dev/loop0 rw extra\n"), nil }
		}},
		{name: "invalid path escape", edit: func(ops *linuxLeaseDetectionOps) {
			ops.readMountInfo = func() ([]byte, error) { return []byte("43 1 8:1 / /tmp\\bad rw - ext4 /dev/loop0 rw\n"), nil }
		}},
		{
			name: "wrong filesystem type",
			edit: func(ops *linuxLeaseDetectionOps) {
				ops.readMountInfo = func() ([]byte, error) {
					return []byte(strings.Replace(validMount, "ext4", "xfs", 1)), nil
				}
			},
		},
		{
			name: "wrong device",
			edit: func(ops *linuxLeaseDetectionOps) {
				ops.readMountInfo = func() ([]byte, error) {
					return []byte(strings.Replace(validMount, "8:1", "8:2", 1)), nil
				}
			},
		},
		{
			name: "duplicate match",
			edit: func(ops *linuxLeaseDetectionOps) {
				ops.readMountInfo = func() ([]byte, error) { return []byte(validMount + validMount), nil }
			},
		},
		{
			name: "wrong superblock magic",
			edit: func(ops *linuxLeaseDetectionOps) {
				ops.fstatfs = func(_ *os.File, fs *unix.Statfs_t) error {
					fs.Type = 0x58465342
					return nil
				}
			},
		},
		{
			name: "missing mount id mask",
			edit: func(ops *linuxLeaseDetectionOps) {
				ops.statx = func(_ *os.File, _ int, stx *unix.Statx_t) error {
					stx.Mask = unix.STATX_INO
					stx.Ino, stx.Dev_major, stx.Dev_minor = 7, 8, 1
					return nil
				}
			},
		},
		{
			name: "mountinfo bound",
			edit: func(ops *linuxLeaseDetectionOps) {
				ops.readMountInfo = func() ([]byte, error) { return make([]byte, maxMountInfoBytes+1), nil }
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ops := base
			tc.edit(&ops)
			if _, ok := detectLinuxExt4LeaseWithOps(file, ops); ok {
				t.Fatal("uncertain ext4 evidence must be unavailable")
			}
		})
	}
}

// TestDetectLinuxExt4LeaseAcceptsUnrelatedNamespaceMountRecords proves target
// selection happens by mount ID before any target-specific path validation.
// nsfs namespace mounts legitimately encode a non-path root such as
// `net:[4026532637]`; validating every record's root/mountpoint rejected the
// whole table and made a valid ext4 source unavailable on Docker/CNI hosts.
// Namespace records sit before *and* after the target so a parser that validates
// the whole table again cannot pass.
func TestDetectLinuxExt4LeaseAcceptsUnrelatedNamespaceMountRecords(t *testing.T) {
	t.Parallel()
	file, err := os.Create(filepath.Join(t.TempDir(), "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()

	const (
		targetMount    = "43 1 8:1 / /tmp rw,relatime - ext4 /dev/loop0 rw\n"
		namespaceFirst = "642 26 0:4 net:[4026532637] /run/netns/ns1 rw shared:330 - nsfs nsfs rw\n"
		namespaceLast  = "643 26 0:5 net:[4026532999] /run/netns/ns2 rw shared:331 - nsfs nsfs rw\n"
	)
	ext4Ops := func(mountID uint64, devMajor, devMinor uint32, mountInfo string) linuxLeaseDetectionOps {
		return linuxLeaseDetectionOps{
			statx: func(_ *os.File, _ int, stx *unix.Statx_t) error {
				*stx = unix.Statx_t{
					Mask: unix.STATX_INO | unix.STATX_MNT_ID,
					Ino:  7, Mnt_id: mountID, Dev_major: devMajor, Dev_minor: devMinor,
				}
				return nil
			},
			fstatfs: func(_ *os.File, fs *unix.Statfs_t) error {
				fs.Type = linuxExt4Magic
				return nil
			},
			readMountInfo: func() ([]byte, error) { return []byte(mountInfo), nil },
		}
	}

	mountInfo := namespaceFirst + targetMount + namespaceLast
	evidence, ok := detectLinuxExt4LeaseWithOps(file, ext4Ops(43, 8, 1, mountInfo))
	if !ok {
		t.Fatal("unrelated nsfs namespace records must not invalidate valid ext4 mount evidence")
	}
	if evidence.identity.Scheme != identitySchemeLinuxExt4Lease || evidence.mountID != 43 {
		t.Fatalf("unexpected evidence for namespace-adjacent mountinfo: evidence=%+v", evidence)
	}

	// Target evidence is unchanged: when the namespace record *is* the target,
	// its non-path root and non-ext4 filesystem type still fail closed.
	if _, ok := detectLinuxExt4LeaseWithOps(file, ext4Ops(642, 0, 4, mountInfo)); ok {
		t.Fatal("a namespace record must not satisfy target ext4 evidence")
	}
}

func TestMountInfoRejectsIncompleteAndConflictingTargetRecords(t *testing.T) {
	t.Parallel()
	valid := "43 1 8:1 / /tmp rw,relatime - ext4 /dev/loop0 rw\n"
	for _, tc := range []struct {
		name string
		data string
	}{
		{name: "unterminated final record", data: strings.TrimSuffix(valid, "\n")},
		{name: "same mount id conflicting device", data: valid + "43 1 8:2 / /other rw - xfs /dev/other rw\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			matched, err := parseExt4MountInfo([]byte(tc.data), 43, 8, 1)
			if err == nil && matched {
				t.Fatalf("accepted incomplete or ambiguous mount evidence: %q", tc.data)
			}
		})
	}
}

func TestSameLeaseDeviceRequiresMajorAndMinorMatch(t *testing.T) {
	t.Parallel()
	active := ActiveSourceVersion{devMajor: 8, devMinor: 1}
	candidate := SourceSnapshot{devMajor: 8, devMinor: 1}
	if !sameLeaseDevice(active, candidate) {
		t.Fatal("same ext4 device should match")
	}
	candidate.devMinor = 2
	if sameLeaseDevice(active, candidate) {
		t.Fatal("different device minor must not satisfy the lease replacement contract")
	}
	candidate.devMajor, candidate.devMinor = 9, 1
	if sameLeaseDevice(active, candidate) {
		t.Fatal("different device major must not satisfy the lease replacement contract")
	}
}
