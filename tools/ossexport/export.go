package main

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	// enterpriseSegment is the path segment that marks the Enterprise tree.
	enterpriseSegment = "enterprise"
	// buildTimeout bounds the build and the vet of the exported tree.
	buildTimeout = 30 * time.Minute
	// maxToolOutput bounds the captured output of a failed Go command.
	maxToolOutput = 4000
)

var (
	// safeRevision keeps the revision argument to characters that cannot carry
	// a Git option or a command.
	safeRevision = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/@{}^~-]*$`)
	// commitID matches a full Git commit id.
	commitID = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

// export archives the requested revision, checks the exported tree, proves it
// builds and vets, and publishes the archive.
func export(ctx context.Context, opts options) (result, error) {
	repo, err := resolveRepository(ctx, opts.repo)
	if err != nil {
		return result{}, err
	}
	sha, err := resolveRevision(ctx, repo, opts.rev)
	if err != nil {
		return result{}, err
	}
	outPath, err := filepath.Abs(opts.out)
	if err != nil {
		return result{}, err
	}
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return result{}, fmt.Errorf("create the output directory: %w", err)
	}
	if info, err := os.Stat(outPath); err == nil && info.IsDir() {
		return result{}, fmt.Errorf("the output path %s is a directory", outPath)
	}

	tree, cleanup, err := prepareTree(outPath, opts.extractDir)
	if err != nil {
		return result{}, err
	}
	defer cleanup()

	archiveFile, err := os.CreateTemp(filepath.Dir(outPath), filepath.Base(outPath)+".tmp-*")
	if err != nil {
		return result{}, fmt.Errorf("create the staging archive: %w", err)
	}
	archivePath := archiveFile.Name()
	if err := archiveFile.Close(); err != nil {
		return result{}, fmt.Errorf("close the staging archive: %w", err)
	}
	published := false
	defer func() {
		if !published {
			_ = os.Remove(archivePath)
		}
	}()

	if err := checkLocalAttributes(ctx, repo); err != nil {
		return result{}, err
	}
	if err := archiveRevision(ctx, repo, sha, archivePath, tree); err != nil {
		return result{}, err
	}
	digest, err := fileDigest(archivePath)
	if err != nil {
		return result{}, err
	}
	files, err := extractArchive(archivePath, tree)
	if err != nil {
		return result{}, err
	}
	tracked, err := trackedFiles(ctx, repo, sha)
	if err != nil {
		return result{}, err
	}
	excluded, included := splitExported(tracked, files)

	modulePath, err := modulePathOf(filepath.Join(tree, "go.mod"))
	if err != nil {
		return result{}, err
	}
	enterpriseModules, err := enterpriseModulePaths(tree)
	if err != nil {
		return result{}, err
	}
	violations, err := checkTree(tree, modulePath, enterpriseModules, excluded, included)
	if err != nil {
		return result{}, err
	}
	if len(violations) > 0 {
		return result{}, &rejectionError{violations: violations}
	}

	buildCtx, cancel := context.WithTimeout(ctx, buildTimeout)
	defer cancel()
	if err := runGoTool(buildCtx, tree, "build", "./..."); err != nil {
		return result{}, fmt.Errorf("the exported tree does not build: %w", err)
	}
	if err := runGoTool(buildCtx, tree, "vet", "./..."); err != nil {
		return result{}, fmt.Errorf("the exported tree does not vet: %w", err)
	}

	if err := os.Chmod(archivePath, 0o644); err != nil {
		return result{}, fmt.Errorf("set the archive mode: %w", err)
	}
	if err := os.Rename(archivePath, outPath); err != nil {
		return result{}, fmt.Errorf("publish the archive: %w", err)
	}
	published = true

	return result{
		Revision: sha,
		Module:   modulePath,
		Archive:  outPath,
		Digest:   digest,
		Files:    len(files),
		Tree:     tree,
	}, nil
}

// prepareTree returns the directory that receives the exported tree and the
// cleanup that removes it again.
func prepareTree(outPath, extractDir string) (string, func(), error) {
	if extractDir == "" {
		dir, err := os.MkdirTemp(filepath.Dir(outPath), ".ossexport-tree-*")
		if err != nil {
			return "", nil, fmt.Errorf("create the staging directory: %w", err)
		}
		return dir, func() { _ = os.RemoveAll(dir) }, nil
	}
	abs, err := filepath.Abs(extractDir)
	if err != nil {
		return "", nil, err
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return "", nil, fmt.Errorf("read the extract directory: %w", err)
		}
		if err := os.MkdirAll(abs, 0o755); err != nil {
			return "", nil, fmt.Errorf("create the extract directory: %w", err)
		}
		return abs, func() {}, nil
	}
	if len(entries) > 0 {
		return "", nil, fmt.Errorf("the extract directory %s is not empty", abs)
	}
	return abs, func() {}, nil
}

// git runs a Git command and returns its standard output.
func git(ctx context.Context, dir string, args ...string) ([]byte, error) {
	return gitWithEnvironment(ctx, dir, nil, args...)
}

// gitWithEnvironment runs a Git command with an explicit environment. A nil
// environment keeps the environment of the caller.
func gitWithEnvironment(ctx context.Context, dir string, environment []string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	if environment != nil {
		cmd.Env = environment
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// archiveEnvironment returns the environment of the archive subprocess. The
// system attributes file of the machine is disabled, because Git reads it
// outside the repository and it would change the archive bytes of one commit.
// The switch is set exactly once, whatever the caller set.
func archiveEnvironment() []string {
	environment := make([]string, 0, len(os.Environ())+1)
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "GIT_ATTR_NOSYSTEM=") {
			continue
		}
		environment = append(environment, entry)
	}
	return append(environment, "GIT_ATTR_NOSYSTEM=1")
}

// resolveRepository returns the root of the repository to export.
func resolveRepository(ctx context.Context, repo string) (string, error) {
	dir := repo
	if dir == "" {
		dir = "."
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	out, err := git(ctx, abs, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("%s is not a Git repository: %w", abs, err)
	}
	top := strings.TrimSpace(string(out))
	if top == "" {
		return "", fmt.Errorf("%s is not a Git repository", abs)
	}
	return top, nil
}

// resolveRevision resolves a revision to a full commit id. The revision never
// reaches Git as an option or a command.
func resolveRevision(ctx context.Context, repo, rev string) (string, error) {
	if rev == "" {
		return "", errors.New("the revision must not be empty")
	}
	if !safeRevision.MatchString(rev) {
		return "", fmt.Errorf("the revision %q contains characters that are not allowed", rev)
	}
	out, err := git(ctx, repo, "rev-parse", "--verify", "--end-of-options", rev+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("the revision %q does not name a commit: %w", rev, err)
	}
	sha := strings.TrimSpace(string(out))
	if !commitID.MatchString(sha) {
		return "", fmt.Errorf("the revision %q resolved to %q, which is not a commit id", rev, sha)
	}
	return sha, nil
}

// archiveRevision writes the tar archive of the revision to dest. Git applies
// the export-ignore attributes of the archived tree.
//
// The archive configuration is pinned, because the ambient configuration of
// the machine changes the archive bytes of the same commit: tar.umask sets
// the entry modes, core.autocrlf changes the line endings of the archived
// files, core.attributesFile adds export-ignore rules from outside the
// archived tree, and the system attributes file of the machine adds them too.
// The pinned values are the documented Git defaults, and the archive
// subprocess disables the system attributes file, so the archive of a commit
// does not change with the machine that produces it.
func archiveRevision(ctx context.Context, repo, sha, dest, staging string) error {
	args := []string{
		"-c", "tar.umask=0002",
		"-c", "core.autocrlf=false",
		"-c", "core.attributesFile=" + filepath.Join(staging, "no-global-attributes"),
		"archive", "--format=tar", "--output=" + dest, sha,
	}
	if _, err := gitWithEnvironment(ctx, repo, archiveEnvironment(), args...); err != nil {
		return fmt.Errorf("archive the revision %s: %w", sha, err)
	}
	return nil
}

// checkLocalAttributes refuses a clone whose local attributes file would add
// rules to the published tree. The export must be a function of the commit
// alone, and a local file is not part of the commit.
func checkLocalAttributes(ctx context.Context, repo string) error {
	out, err := git(ctx, repo, "rev-parse", "--git-common-dir")
	if err != nil {
		return fmt.Errorf("locate the Git directory: %w", err)
	}
	directory := strings.TrimSpace(string(out))
	if directory == "" {
		return errors.New("the Git directory is unknown")
	}
	if !filepath.IsAbs(directory) {
		directory = filepath.Join(repo, directory)
	}
	path := filepath.Join(directory, "info", "attributes")
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read %s: %w", path, err)
	}
	if info.Size() == 0 {
		return nil
	}
	return fmt.Errorf("the local attributes file %s adds rules to the exported tree: the export must be a function of the commit alone, so remove the file or keep the rules in the committed tree", path)
}

// trackedFiles lists the tracked files of the revision.
func trackedFiles(ctx context.Context, repo, sha string) ([]string, error) {
	out, err := git(ctx, repo, "ls-tree", "-r", "-z", "--name-only", sha)
	if err != nil {
		return nil, fmt.Errorf("list the tracked files: %w", err)
	}
	var files []string
	for name := range strings.SplitSeq(string(out), "\x00") {
		if name != "" {
			files = append(files, name)
		}
	}
	sort.Strings(files)
	return files, nil
}

// splitExported divides the tracked files of the revision into the files the
// export drops and the files it keeps.
func splitExported(tracked, exported []string) (excluded, included []string) {
	kept := make(map[string]bool, len(exported))
	for _, name := range exported {
		kept[name] = true
	}
	for _, name := range tracked {
		if kept[name] {
			included = append(included, name)
			continue
		}
		excluded = append(excluded, name)
	}
	sort.Strings(excluded)
	sort.Strings(included)
	return excluded, included
}

// extractArchive extracts an archive into dest.
func extractArchive(archive, dest string) ([]string, error) {
	file, err := os.Open(archive)
	if err != nil {
		return nil, fmt.Errorf("open the archive: %w", err)
	}
	defer func() { _ = file.Close() }()
	return extractTar(file, dest)
}

// extractTar extracts a tar stream into dest and returns the extracted file
// paths. It refuses entries that would write outside dest and entries that
// are not regular files, so a symlink is reported instead of dropped.
func extractTar(r io.Reader, dest string) ([]string, error) {
	reader := tar.NewReader(r)
	var files []string
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read the archive: %w", err)
		}
		relative, err := safeArchivePath(header.Name)
		if err != nil {
			return nil, err
		}
		target := filepath.Join(dest, filepath.FromSlash(relative))
		if err := checkInside(dest, target); err != nil {
			return nil, err
		}
		switch header.Typeflag {
		case tar.TypeXGlobalHeader, tar.TypeXHeader:
			// Metadata records, for example the commit id Git writes into the
			// archive, carry no tree content.
			continue
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return nil, fmt.Errorf("create the directory %s: %w", relative, err)
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return nil, fmt.Errorf("create the directory of %s: %w", relative, err)
			}
			mode := os.FileMode(0o644)
			if header.Mode&0o111 != 0 {
				mode = 0o755
			}
			file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
			if err != nil {
				return nil, fmt.Errorf("create the file %s: %w", relative, err)
			}
			if _, err := io.Copy(file, reader); err != nil {
				_ = file.Close()
				return nil, fmt.Errorf("write the file %s: %w", relative, err)
			}
			if err := file.Close(); err != nil {
				return nil, fmt.Errorf("close the file %s: %w", relative, err)
			}
			files = append(files, relative)
		default:
			return nil, fmt.Errorf("the archive entry %q has the unsupported type %q: the export refuses symlinks, hard links, and device entries",
				header.Name, string(header.Typeflag))
		}
	}
	return files, nil
}

// safeArchivePath normalises an archive entry name and refuses a name that
// leaves the export root.
func safeArchivePath(name string) (string, error) {
	if name == "" {
		return "", errors.New("the archive holds an entry without a name")
	}
	if strings.HasPrefix(name, "/") {
		return "", fmt.Errorf("the archive entry %q uses an absolute path", name)
	}
	cleaned := path.Clean(name)
	if cleaned == "." || cleaned == "" {
		return "", fmt.Errorf("the archive entry %q does not name a file", name)
	}
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("the archive entry %q escapes the export root", name)
	}
	if slices.Contains(strings.Split(cleaned, "/"), "..") {
		return "", fmt.Errorf("the archive entry %q escapes the export root", name)
	}
	return cleaned, nil
}

// checkInside refuses a path that leaves the export root.
func checkInside(root, target string) error {
	relative, err := filepath.Rel(root, target)
	if err != nil {
		return fmt.Errorf("the archive entry %q escapes the export root", target)
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("the archive entry %q escapes the export root", target)
	}
	return nil
}

// fileDigest returns the SHA-256 digest of a file.
func fileDigest(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = file.Close() }()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

// modulePathOf reads the module path a go.mod declares.
func modulePathOf(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read the module file: %w", err)
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) < 2 || fields[0] != "module" || fields[1] == "(" {
			continue
		}
		modulePath := fields[1]
		if unquoted, err := strconv.Unquote(modulePath); err == nil {
			modulePath = unquoted
		}
		return modulePath, nil
	}
	return "", fmt.Errorf("%s does not declare a module path", path)
}

// enterpriseModulePaths returns the module paths of the Enterprise modules of
// the exported tree.
func enterpriseModulePaths(root string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("read the export root: %w", err)
	}
	var paths []string
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() != enterpriseSegment {
			continue
		}
		moduleFile := filepath.Join(root, enterpriseSegment, "go.mod")
		modulePath, err := modulePathOf(moduleFile)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, err
		}
		paths = append(paths, modulePath)
	}
	return paths, nil
}

// runGoTool runs a Go command in a directory and reports its output on
// failure.
func runGoTool(ctx context.Context, dir string, args ...string) error {
	command := exec.CommandContext(ctx, "go", args...)
	command.Dir = dir
	command.Env = os.Environ()
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Run(); err != nil {
		return fmt.Errorf("go %s: %w\n%s", strings.Join(args, " "), err, truncate(output.String(), maxToolOutput))
	}
	return nil
}

// truncate shortens a captured command output.
func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "\n[output truncated]"
}
