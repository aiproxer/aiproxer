package discovery

import (
	"errors"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	inframanifest "github.com/aiproxer/aiproxer/internal/infra/backendplugins/manifest"
	sdkmanifest "github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin/manifest"
)

var (
	ErrDevelopmentPathsRequired = errors.New("backendplugins/discovery: development mode requires explicit paths")
	ErrNoRoots                  = errors.New("backendplugins/discovery: no discovery roots configured")
)

// Status is a bounded discovery outcome for one manifest path.
type Status string

const (
	StatusDiscovered Status = "discovered"
	StatusInvalid    Status = "invalid"
	StatusSkipped    Status = "skipped"
)

// Descriptor is an immutable discovery record.
type Descriptor struct {
	SafeID       string
	Root         string
	ManifestPath string
	Manifest     sdkmanifest.Manifest
	Status       Status
	Reason       string
}

// Result is the immutable discovery snapshot.
type Result struct {
	Descriptors []Descriptor
}

// Discover scans configured roots for regular *.backendplugin.json manifests.
// It never launches processes, searches PATH/CWD, or touches the network.
func Discover(cfg Config) (Result, error) {
	roots, err := cfg.roots()
	if err != nil {
		return Result{}, err
	}
	if len(roots) == 0 {
		return Result{}, ErrNoRoots
	}
	fsys := cfg.FS
	if fsys == nil {
		fsys = osFS{}
	}
	var out []Descriptor
	seenManifest := map[string]struct{}{}
	for _, root := range roots {
		info, err := fsys.Lstat(root)
		if err != nil || !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 {
			continue
		}
		absRoot, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		entries, err := fsys.ReadDir(root)
		if err != nil {
			continue
		}
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		sort.Strings(names)
		for _, name := range names {
			if !strings.HasSuffix(name, ".backendplugin.json") {
				continue
			}
			mp := filepath.Join(root, name)
			key := filepath.Clean(mp)
			if _, ok := seenManifest[key]; ok {
				continue
			}
			seenManifest[key] = struct{}{}
			desc := scanManifest(fsys, absRoot, root, name, mp)
			out = append(out, desc)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].SafeID != out[j].SafeID {
			return out[i].SafeID < out[j].SafeID
		}
		return out[i].ManifestPath < out[j].ManifestPath
	})
	return Result{Descriptors: out}, nil
}

func scanManifest(fsys FS, absRoot, root, name, mp string) Descriptor {
	base := Descriptor{SafeID: safeID(root, name), Root: root, ManifestPath: mp}
	fi, err := fsys.Lstat(mp)
	if err != nil {
		base.Status = StatusSkipped
		base.Reason = "lstat_failed"
		return base
	}
	if fi.Mode()&fs.ModeSymlink != 0 {
		base.Status = StatusSkipped
		base.Reason = "symlink_rejected"
		return base
	}
	if !fi.Mode().IsRegular() {
		base.Status = StatusSkipped
		base.Reason = "not_regular_file"
		return base
	}
	if _, err := underRoot(absRoot, mp); err != nil {
		base.Status = StatusSkipped
		base.Reason = "path_escape"
		return base
	}
	if fi.Size() > int64(sdkmanifest.MaxManifestBytes) {
		base.Status = StatusInvalid
		base.Reason = "manifest_too_large"
		return base
	}
	f, err := fsys.Open(mp)
	if err != nil {
		base.Status = StatusSkipped
		base.Reason = "open_failed"
		if errors.Is(err, ErrSymlinkRejected) {
			base.Reason = "symlink_rejected"
		}
		return base
	}
	defer func() { _ = f.Close() }()
	// Re-check identity after open: still under root and not a symlink target escape.
	openedName := f.Name()
	if _, err := underRoot(absRoot, openedName); err != nil {
		base.Status = StatusSkipped
		base.Reason = "path_escape"
		return base
	}
	ofi, err := f.Stat()
	if err != nil || !ofi.Mode().IsRegular() {
		base.Status = StatusSkipped
		base.Reason = "not_regular_file"
		return base
	}
	raw, err := openBounded(f, int64(sdkmanifest.MaxManifestBytes))
	if err != nil {
		base.Status = StatusInvalid
		base.Reason = "read_failed"
		return base
	}
	m, err := inframanifest.ParseStrictBytes(raw)
	if err != nil {
		base.Status = StatusInvalid
		base.Reason = "parse_failed"
		return base
	}
	base.Manifest = m
	base.Status = StatusDiscovered
	base.Reason = "ok"
	return base
}

func safeID(root, name string) string {
	return filepath.Base(root) + "/" + name
}
