package cache

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/wnarutou/gitrieve/internal/scm"
	"github.com/wnarutou/gitrieve/internal/storage"
	"github.com/wnarutou/gitrieve/internal/typedef"
)

type Component string

const (
	ComponentCode       Component = "code"
	ComponentWiki       Component = "wiki"
	ComponentIssues     Component = "issues"
	ComponentDiscussion Component = "discussion"
)

type Status string

const (
	StatusRestored       Status = "restored"
	StatusAlreadyPresent Status = "already_present"
	StatusNotFound       Status = "not_found"
)

const (
	cacheMetadataFile    = ".gitrieve-cache.json"
	cacheMetadataVersion = 1
)

type cacheMetadata struct {
	Version     int       `json:"version"`
	Component   Component `json:"component"`
	CompletedAt time.Time `json:"completed_at"`
}

// Layout describes where one component's backup archive lives relative to a
// storage backend and where it must be restored in the local working cache.
type Layout struct {
	Archive string
	Target  string
	Root    string
}

type Result struct {
	Component   Component
	Status      Status
	Archive     string
	Target      string
	StorageName string
}

func LayoutFor(currentDir string, r *scm.Repository, component Component) (Layout, error) {
	if currentDir == "" {
		return Layout{}, errors.New("restore: current directory is empty")
	}
	if r == nil || r.Name == "" {
		return Layout{}, errors.New("restore: repository name is empty")
	}

	repoDir := filepath.Join(currentDir, ".gitrieve", r.Host, r.Owner, r.Name)
	switch component {
	case ComponentCode:
		return Layout{
			Archive: r.Name + ".tar.gz",
			Target:  filepath.Join(repoDir, "code"),
			Root:    r.Name,
		}, nil
	case ComponentWiki:
		return Layout{
			Archive: r.Name + "_wiki.tar.gz",
			Target:  filepath.Join(repoDir, "wiki"),
			Root:    r.Name + "_wiki",
		}, nil
	case ComponentIssues:
		return Layout{
			Archive: "issues.tar.gz",
			Target:  filepath.Join(repoDir, "issues"),
			Root:    "issues",
		}, nil
	case ComponentDiscussion:
		return Layout{
			Archive: "discussions.tar.gz",
			Target:  filepath.Join(repoDir, "discussion"),
			Root:    "discussion",
		}, nil
	default:
		return Layout{}, fmt.Errorf("restore: unsupported component %q", component)
	}
}

// RestoreComponent restores one component's local cache from the first
// configured storage that contains a usable archive. A missing archive is a
// normal result, not an error, because first-time backups have nothing to
// restore yet.
func RestoreComponent(
	ctx context.Context,
	currentDir string,
	r *scm.Repository,
	component Component,
	storages []typedef.MultiStorage,
) (Result, error) {
	layout, err := LayoutFor(currentDir, r, component)
	if err != nil {
		return Result{}, err
	}
	result := Result{
		Component: component,
		Archive:   layout.Archive,
		Target:    layout.Target,
	}
	if cachePresent(layout.Target, component) {
		result.Status = StatusAlreadyPresent
		return result, nil
	}

	var restoreErrs []error
	for _, s := range storages {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		backend, err := storage.GetStorage(s)
		if err != nil {
			restoreErrs = append(restoreErrs, fmt.Errorf("storage %s: %w", s.Name, err))
			continue
		}
		identifier := path.Join(s.Path, r.Host, r.Owner, r.Name, layout.Archive)
		if err := restoreObject(ctx, backend, identifier, layout, component); err != nil {
			if storage.IsNotFound(err) {
				continue
			}
			restoreErrs = append(restoreErrs, fmt.Errorf("storage %s: %w", s.Name, err))
			continue
		}
		result.Status = StatusRestored
		result.StorageName = s.Name
		return result, nil
	}
	if len(restoreErrs) > 0 {
		return result, errors.Join(restoreErrs...)
	}
	result.Status = StatusNotFound
	return result, nil
}

func cachePresent(target string, component Component) bool {
	switch component {
	case ComponentCode, ComponentWiki:
		return validateGitCache(target) == nil
	case ComponentIssues, ComponentDiscussion:
		if metadata, exists := readCacheMetadata(target); exists {
			return metadata.Version == cacheMetadataVersion && metadata.Component == component
		}
		entries, err := os.ReadDir(target)
		return err == nil && len(entries) > 0
	default:
		return false
	}
}

// Present reports whether a component already has a usable local cache.
func Present(currentDir string, r *scm.Repository, component Component) bool {
	layout, err := LayoutFor(currentDir, r, component)
	if err != nil {
		return false
	}
	return cachePresent(layout.Target, component)
}

// MarkPresent records that a non-Git cache component completed successfully.
// The marker distinguishes a legitimate empty issue/discussion cache from a
// directory that is missing or only partially written.
func MarkPresent(target string, component Component) error {
	if component != ComponentIssues && component != ComponentDiscussion {
		return nil
	}
	if err := os.MkdirAll(target, 0o775); err != nil {
		return fmt.Errorf("create cache directory: %w", err)
	}
	data, err := json.Marshal(cacheMetadata{
		Version:     cacheMetadataVersion,
		Component:   component,
		CompletedAt: time.Now().UTC(),
	})
	if err != nil {
		return fmt.Errorf("encode cache metadata: %w", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(filepath.Join(target, cacheMetadataFile), data, 0o664); err != nil {
		return fmt.Errorf("write cache metadata: %w", err)
	}
	return nil
}

func readCacheMetadata(target string) (cacheMetadata, bool) {
	data, err := os.ReadFile(filepath.Join(target, cacheMetadataFile))
	if err != nil {
		return cacheMetadata{}, false
	}
	var metadata cacheMetadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		return cacheMetadata{}, true
	}
	return metadata, true
}

func restoreObject(
	ctx context.Context,
	backend storage.Storage,
	identifier string,
	layout Layout,
	component Component,
) error {
	expectedChecksum, err := readExpectedChecksum(backend, identifier+".sha256")
	if err != nil {
		return err
	}
	reader, err := openObject(backend, identifier)
	if err != nil {
		return err
	}
	defer reader.Close()

	parent := filepath.Dir(layout.Target)
	if err := os.MkdirAll(parent, 0o775); err != nil {
		return fmt.Errorf("create cache parent: %w", err)
	}
	stagingParent, err := os.MkdirTemp(parent, ".restore-"+string(component)+"-")
	if err != nil {
		return fmt.Errorf("create restore staging directory: %w", err)
	}
	defer os.RemoveAll(stagingParent)

	stagingRoot := filepath.Join(stagingParent, layout.Root)
	if err := os.MkdirAll(stagingRoot, 0o755); err != nil {
		return fmt.Errorf("create restore target: %w", err)
	}
	hasher := sha256.New()
	verifiedReader := io.TeeReader(reader, hasher)
	if err := extractTarGz(ctx, verifiedReader, stagingRoot, layout.Root); err != nil {
		return err
	}
	if _, err := io.Copy(io.Discard, verifiedReader); err != nil {
		return fmt.Errorf("finish checksum read: %w", err)
	}
	if expectedChecksum != "" {
		actual := hex.EncodeToString(hasher.Sum(nil))
		if !strings.EqualFold(actual, expectedChecksum) {
			return fmt.Errorf("restore: checksum mismatch for %s: expected %s, got %s", identifier, expectedChecksum, actual)
		}
	}
	if err := validateRestored(stagingRoot, component); err != nil {
		return err
	}

	if err := os.RemoveAll(layout.Target); err != nil {
		return fmt.Errorf("remove stale cache: %w", err)
	}
	if err := os.Rename(stagingRoot, layout.Target); err != nil {
		return fmt.Errorf("install restored cache: %w", err)
	}
	if component == ComponentIssues || component == ComponentDiscussion {
		if err := MarkPresent(layout.Target, component); err != nil {
			return err
		}
	}
	return nil
}

func readExpectedChecksum(backend storage.Storage, identifier string) (string, error) {
	reader, err := openObject(backend, identifier)
	if err != nil {
		if storage.IsNotFound(err) {
			return "", nil
		}
		return "", fmt.Errorf("read archive checksum: %w", err)
	}
	defer reader.Close()

	data, err := io.ReadAll(io.LimitReader(reader, 257))
	if err != nil {
		return "", fmt.Errorf("read archive checksum: %w", err)
	}
	if len(data) > 256 {
		return "", fmt.Errorf("restore: archive checksum file %s is too large", identifier)
	}
	value := strings.TrimSpace(string(data))
	if value == "" {
		return "", fmt.Errorf("restore: archive checksum file %s is empty", identifier)
	}
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != sha256.Size {
		return "", fmt.Errorf("restore: archive checksum file %s is invalid", identifier)
	}
	return value, nil
}

func openObject(backend storage.Storage, identifier string) (io.ReadCloser, error) {
	if reader, ok := backend.(storage.ObjectReader); ok {
		return reader.OpenObject(identifier)
	}
	object, err := backend.GetObject(identifier)
	if err != nil {
		return nil, err
	}
	return io.NopCloser(bytes.NewReader(object.Content)), nil
}

func extractTarGz(ctx context.Context, source io.Reader, stagingRoot, archiveRoot string) error {
	gz, err := gzip.NewReader(source)
	if err != nil {
		return fmt.Errorf("open gzip archive: %w", err)
	}
	defer gz.Close()

	type pendingSymlink struct {
		target string
		link   string
	}
	var symlinks []pendingSymlink

	tr := tar.NewReader(gz)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read archive entry: %w", err)
		}

		relative, isRoot, err := archiveRelativePath(header.Name, archiveRoot)
		if err != nil {
			return err
		}
		if isRoot {
			continue
		}
		target, err := secureJoin(stagingRoot, relative)
		if err != nil {
			return err
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, directoryMode(header.Mode)); err != nil {
				return fmt.Errorf("create directory %s: %w", relative, err)
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := writeArchiveFile(tr, target, header); err != nil {
				return fmt.Errorf("extract %s: %w", relative, err)
			}
		case tar.TypeSymlink:
			if safeSymlinkTarget(stagingRoot, target, header.Linkname) {
				symlinks = append(symlinks, pendingSymlink{target: target, link: header.Linkname})
			}
		}
	}

	// Symlinks are created only after all regular entries so no file write can
	// traverse a symlink created earlier in the same archive.
	for _, symlink := range symlinks {
		if err := os.MkdirAll(filepath.Dir(symlink.target), 0o755); err != nil {
			return err
		}
		// Symlink creation is best-effort: Windows without Developer Mode cannot
		// create them, but the .git cache and regular worktree files are still
		// usable and the next successful checkout repairs missing links.
		_ = os.Symlink(symlink.link, symlink.target)
	}
	return nil
}

func archiveRelativePath(name, archiveRoot string) (string, bool, error) {
	if name == "" {
		return "", false, errors.New("restore: archive contains an empty path")
	}
	normalized := strings.ReplaceAll(name, "\\", "/")
	native := filepath.FromSlash(normalized)
	if strings.HasPrefix(normalized, "/") || filepath.IsAbs(native) || filepath.VolumeName(native) != "" {
		return "", false, fmt.Errorf("restore: path traversal in archive entry %q", name)
	}
	for _, part := range strings.Split(normalized, "/") {
		if part == ".." {
			return "", false, fmt.Errorf("restore: path traversal in archive entry %q", name)
		}
	}

	cleaned := path.Clean(normalized)
	if cleaned == "." {
		return "", true, nil
	}
	if cleaned == archiveRoot {
		return "", true, nil
	}
	prefix := archiveRoot + "/"
	if !strings.HasPrefix(cleaned, prefix) {
		return "", false, fmt.Errorf("restore: unexpected archive root %q, want %q", name, archiveRoot)
	}
	return strings.TrimPrefix(cleaned, prefix), false, nil
}

func secureJoin(root, relative string) (string, error) {
	target := filepath.Join(root, filepath.FromSlash(relative))
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return "", fmt.Errorf("restore: resolve archive path: %w", err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("restore: path traversal in archive entry %q", relative)
	}
	return target, nil
}

func writeArchiveFile(source io.Reader, target string, header *tar.Header) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	mode := os.FileMode(header.Mode).Perm()
	if mode == 0 {
		mode = 0o644
	}
	file, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(file, source); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func directoryMode(mode int64) os.FileMode {
	perm := os.FileMode(mode).Perm()
	if perm == 0 {
		return 0o755
	}
	return perm
}

func safeSymlinkTarget(root, target, link string) bool {
	if link == "" || filepath.IsAbs(link) || filepath.VolumeName(link) != "" {
		return false
	}
	resolved := filepath.Clean(filepath.Join(filepath.Dir(target), filepath.FromSlash(link)))
	rel, err := filepath.Rel(root, resolved)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func validateRestored(root string, component Component) error {
	switch component {
	case ComponentCode, ComponentWiki:
		return validateGitCache(root)
	case ComponentIssues, ComponentDiscussion:
		info, err := os.Stat(root)
		if err != nil {
			return fmt.Errorf("restore: archive target is missing: %w", err)
		}
		if !info.IsDir() {
			return errors.New("restore: archive target is not a directory")
		}
		if metadata, exists := readCacheMetadata(root); exists &&
			(metadata.Version != cacheMetadataVersion || metadata.Component != component) {
			return fmt.Errorf("restore: invalid %s cache metadata", component)
		}
	}
	return nil
}

func validateGitCache(root string) error {
	gitDir := filepath.Join(root, ".git")
	for _, required := range []string{"HEAD", "config"} {
		info, err := os.Stat(filepath.Join(gitDir, required))
		if err != nil {
			return fmt.Errorf("restore: incomplete git cache: %s: %w", required, err)
		}
		if info.IsDir() {
			return fmt.Errorf("restore: incomplete git cache: %s is a directory", required)
		}
	}
	for _, required := range []string{"objects", "refs"} {
		info, err := os.Stat(filepath.Join(gitDir, required))
		if err != nil {
			return fmt.Errorf("restore: incomplete git cache: %s: %w", required, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("restore: incomplete git cache: %s is not a directory", required)
		}
	}

	repository, err := git.PlainOpen(root)
	if err != nil {
		return fmt.Errorf("restore: open git cache: %w", err)
	}
	head, err := repository.Head()
	if errors.Is(err, plumbing.ErrReferenceNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("restore: read git HEAD: %w", err)
	}
	if _, err := repository.CommitObject(head.Hash()); err != nil {
		return fmt.Errorf("restore: read HEAD commit: %w", err)
	}
	return nil
}
