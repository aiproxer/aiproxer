package archtest

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/aiproxer/aiproxer/internal/testkit/gitscope"
)

// archtestFS is an abstraction over file reading/walking to support both
// the mutable working tree and pinned Git commit trees.
type archtestFS interface {
	ReadFile(rel string) ([]byte, error)
	WalkProductionGoFiles(fn func(rel string, src []byte) error) error
	WalkRootFiles(rootPath string, fn func(rel string, src []byte) error) error
	ReadDir(rel string) ([]os.DirEntry, error)
}

// workingTreeFS reads directly from the OS working tree.
type workingTreeFS struct {
	root string
}

func (w *workingTreeFS) ReadFile(rel string) ([]byte, error) {
	return os.ReadFile(filepath.Join(w.root, filepath.FromSlash(rel)))
}

func (w *workingTreeFS) WalkProductionGoFiles(fn func(rel string, src []byte) error) error {
	return WalkProductionGoFiles(w.root, func(rel, abs string, src []byte) error {
		return fn(rel, src)
	})
}

func (w *workingTreeFS) WalkRootFiles(rootPath string, fn func(rel string, src []byte) error) error {
	dir := filepath.Join(w.root, filepath.FromSlash(rootPath))
	return filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(w.root, path)
		if rerr != nil {
			return rerr
		}
		rel = filepath.ToSlash(rel)
		if !strings.HasSuffix(rel, ".go") {
			return nil
		}
		src, serr := os.ReadFile(path)
		if serr != nil {
			return serr
		}
		return fn(rel, src)
	})
}

func (w *workingTreeFS) ReadDir(rel string) ([]os.DirEntry, error) {
	return os.ReadDir(filepath.Join(w.root, filepath.FromSlash(rel)))
}

// gitCommitFS reads from an in-memory map populated via git archive.
type gitCommitFS struct {
	sha   string
	files map[string][]byte
}

var (
	gitCommitFSCacheMu sync.Mutex
	gitCommitFSCache   = make(map[string]*gitCommitFSEntry)
)

type gitCommitFSEntry struct {
	done chan struct{}
	fs   *gitCommitFS
	err  error
}

func loadGitCommitFSContext(ctx context.Context, root string, sha string) (*gitCommitFS, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key := root + "\x00" + sha
	gitCommitFSCacheMu.Lock()
	if entry, ok := gitCommitFSCache[key]; ok {
		gitCommitFSCacheMu.Unlock()
		select {
		case <-entry.done:
			return entry.fs, entry.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	entry := &gitCommitFSEntry{done: make(chan struct{})}
	gitCommitFSCache[key] = entry
	gitCommitFSCacheMu.Unlock()

	defer func() {
		if entry.err != nil && (ctx.Err() != nil || errors.Is(entry.err, context.Canceled) || errors.Is(entry.err, context.DeadlineExceeded)) {
			gitCommitFSCacheMu.Lock()
			if gitCommitFSCache[key] == entry {
				delete(gitCommitFSCache, key)
			}
			gitCommitFSCacheMu.Unlock()
		}
		close(entry.done)
	}()

	cmd := exec.CommandContext(ctx, "git", "-C", root, "archive", "--format=tar", sha)
	// root selects the repository whose commit tree is archived. Git exports
	// GIT_DIR to every hook it runs, so an inherited GIT_DIR would archive the
	// ambient repository's tree instead of the pinned one under root.
	cmd.Env = gitscope.Environ()
	var out bytes.Buffer
	var errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	if err := cmd.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			entry.err = ctxErr
			return nil, entry.err
		}
		entry.err = fmt.Errorf("git archive for SHA %s failed: %w (stderr: %s)", sha, err, errOut.String())
		return nil, entry.err
	}

	files := make(map[string][]byte)
	tr := tar.NewReader(&out)
	for {
		if err := ctx.Err(); err != nil {
			entry.err = err
			return nil, err
		}
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			entry.err = err
			return nil, err
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		var content bytes.Buffer
		if _, err := io.Copy(&content, tr); err != nil {
			entry.err = err
			return nil, err
		}
		files[filepath.ToSlash(hdr.Name)] = content.Bytes()
	}
	entry.fs = &gitCommitFS{sha: sha, files: files}
	return entry.fs, nil
}

func (g *gitCommitFS) ReadFile(rel string) ([]byte, error) {
	rel = filepath.ToSlash(rel)
	content, ok := g.files[rel]
	if !ok {
		// A pinned baseline older than the AIProxer rename holds the file
		// under its former path.
		if content, ok = g.files[preRenamePath(rel)]; !ok {
			return nil, os.ErrNotExist
		}
	}
	return content, nil
}

func (g *gitCommitFS) WalkProductionGoFiles(fn func(rel string, src []byte) error) error {
	var paths []string
	for p := range g.files {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	for _, rel := range paths {
		inProductionRoot := false
		for _, top := range ProductionScanRoots() {
			if rel == top || strings.HasPrefix(rel, top+"/") {
				inProductionRoot = true
				break
			}
		}
		if !inProductionRoot {
			continue
		}

		if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") {
			continue
		}

		skip := false
		for seg := range strings.SplitSeq(rel, "/") {
			if seg == "vendor" || seg == "testdata" || seg == "node_modules" {
				skip = true
				break
			}
		}
		if skip {
			continue
		}

		if err := fn(rel, g.files[rel]); err != nil {
			return err
		}
	}
	return nil
}

func (g *gitCommitFS) WalkRootFiles(rootPath string, fn func(rel string, src []byte) error) error {
	rootPath = filepath.ToSlash(rootPath)
	var paths []string
	for p := range g.files {
		if p == rootPath || strings.HasPrefix(p, rootPath+"/") {
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)

	for _, rel := range paths {
		if !strings.HasSuffix(rel, ".go") {
			continue
		}
		if err := fn(rel, g.files[rel]); err != nil {
			return err
		}
	}
	return nil
}

type gitDirEntry struct {
	name  string
	isDir bool
}

func (e gitDirEntry) Name() string               { return e.name }
func (e gitDirEntry) IsDir() bool                { return e.isDir }
func (e gitDirEntry) Type() os.FileMode          { return 0 }
func (e gitDirEntry) Info() (os.FileInfo, error) { return nil, nil }

func (g *gitCommitFS) ReadDir(rel string) ([]os.DirEntry, error) {
	rel = filepath.ToSlash(rel)
	prefix := rel + "/"
	if rel == "." || rel == "" {
		prefix = ""
	}

	seenDirs := make(map[string]bool)
	seenFiles := make(map[string]bool)

	for p := range g.files {
		if prefix != "" && !strings.HasPrefix(p, prefix) {
			continue
		}
		suffix := p[len(prefix):]
		parts := strings.Split(suffix, "/")
		if len(parts) == 0 || parts[0] == "" {
			continue
		}
		if len(parts) > 1 {
			seenDirs[parts[0]] = true
		} else {
			seenFiles[parts[0]] = true
		}
	}

	var out []os.DirEntry
	for name := range seenDirs {
		out = append(out, gitDirEntry{name: name, isDir: true})
	}
	for name := range seenFiles {
		out = append(out, gitDirEntry{name: name, isDir: false})
	}

	sort.Slice(out, func(i, j int) bool {
		return out[i].Name() < out[j].Name()
	})

	return out, nil
}
