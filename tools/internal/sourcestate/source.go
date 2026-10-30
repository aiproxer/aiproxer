// Package sourcestate binds check evidence to Git HEAD, index and working bytes.
package sourcestate

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/aiproxer/aiproxer/internal/testkit/gitscope"
)

// State identifies a Git revision and its staged/working content fingerprint.
type State struct {
	Head        string `json:"head"`
	Fingerprint string `json:"fingerprint"`
}

// Snapshot identifies HEAD plus both diffs and untracked bytes. It is read-only
// and honors Git's ignored-file boundary; keep handoff artifacts outside the repo.
func Snapshot(ctx context.Context, repo string) (State, error) {
	return SnapshotWithIndex(ctx, repo, "")
}

// SnapshotWithIndex includes an explicit alternate index for partial-commit gates.
func SnapshotWithIndex(ctx context.Context, repo, index string) (State, error) {
	git := func(args ...string) ([]byte, error) { return sourceGitWithIndex(ctx, repo, index, args...) }
	head, err := git("rev-parse", "--verify", "HEAD")
	if err != nil {
		return State{}, err
	}
	state := State{Head: strings.TrimSpace(string(head))}
	digest := sha256.New()
	hashPart(digest, []byte(state.Head))
	for _, args := range [][]string{
		{"diff", "--cached", "--binary", "--no-ext-diff", "--no-textconv", "--"},
		{"diff", "--binary", "--no-ext-diff", "--no-textconv", "--"},
	} {
		data, err := git(args...)
		if err != nil {
			return State{}, err
		}
		hashPart(digest, data)
	}
	data, err := git("ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return State{}, err
	}
	names := strings.Split(strings.TrimSuffix(string(data), "\x00"), "\x00")
	slices.Sort(names)
	for _, name := range names {
		if name == "" {
			continue
		}
		hashPart(digest, []byte(name))
		file, err := os.Open(filepath.Join(repo, filepath.FromSlash(name)))
		if err != nil {
			return State{}, err
		}
		content := sha256.New()
		_, copyErr := io.Copy(content, file)
		closeErr := file.Close()
		if copyErr != nil {
			return State{}, copyErr
		}
		if closeErr != nil {
			return State{}, closeErr
		}
		hashPart(digest, content.Sum(nil))
	}
	state.Fingerprint = fmt.Sprintf("%x", digest.Sum(nil))
	return state, nil
}

func hashPart(digest hash.Hash, data []byte) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(data)))
	_, _ = digest.Write(size[:])
	_, _ = digest.Write(data)
}

func sourceGitWithIndex(ctx context.Context, repo, index string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = repo
	cmd.Env = gitscope.Environ()
	if index != "" {
		cmd.Env = append(cmd.Env, "GIT_INDEX_FILE="+index)
	}
	data, err := cmd.Output()
	if err != nil {
		if exit, ok := errors.AsType[*exec.ExitError](err); ok {
			return nil, fmt.Errorf("git %v: %w: %s", args, err, strings.TrimSpace(string(exit.Stderr)))
		}
		return nil, fmt.Errorf("git %v: %w", args, err)
	}
	return data, nil
}
