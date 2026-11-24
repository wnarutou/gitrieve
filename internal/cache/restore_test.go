package cache

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/stretchr/testify/require"
	"github.com/wnarutou/gitrieve/internal/archive"
	"github.com/wnarutou/gitrieve/internal/scm"
	"github.com/wnarutou/gitrieve/internal/typedef"
)

type archiveEntry struct {
	name string
	body string
}

func writeTarGz(t *testing.T, filename string, entries []archiveEntry) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(filename), 0o755))

	file, err := os.Create(filename)
	require.NoError(t, err)
	t.Cleanup(func() { _ = file.Close() })

	gz := gzip.NewWriter(file)
	tw := tar.NewWriter(gz)
	for _, entry := range entries {
		header := &tar.Header{
			Name: entry.name,
			Mode: 0o644,
			Size: int64(len(entry.body)),
		}
		require.NoError(t, tw.WriteHeader(header))
		_, err := tw.Write([]byte(entry.body))
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())
	require.NoError(t, file.Close())
}

func writeGitBackup(t *testing.T, filename, rootName string) {
	t.Helper()
	source := filepath.Join(t.TempDir(), rootName)
	_, err := git.PlainInit(source, false)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(source, "main.go"), []byte("package main\n"), 0o644))
	buf, err := archive.Create(context.Background(), source, rootName)
	require.NoError(t, err)
	defer buf.Close()
	require.NoError(t, os.MkdirAll(filepath.Dir(filename), 0o755))
	destination, err := os.Create(filename)
	require.NoError(t, err)
	_, err = io.Copy(destination, buf)
	require.NoError(t, err)
	require.NoError(t, destination.Close())
}

func fileStorages(root string) []typedef.MultiStorage {
	return []typedef.MultiStorage{{
		Storage: typedef.Storage{Name: "local", Type: "file", Path: root},
	}}
}

func TestRestoreComponentRestoresCodeCacheIncludingGitMetadata(t *testing.T) {
	base := t.TempDir()
	cwd := filepath.Join(base, "app")
	storageRoot := filepath.Join(base, "repo")
	require.NoError(t, os.MkdirAll(cwd, 0o755))

	writeGitBackup(t, filepath.Join(storageRoot, "github.com", "acme", "demo", "demo.tar.gz"), "demo")

	r, err := scm.NewRepository("github.com/acme/demo")
	require.NoError(t, err)

	result, err := RestoreComponent(
		context.Background(), cwd, r, ComponentCode, fileStorages(storageRoot),
	)
	require.NoError(t, err)
	require.Equal(t, StatusRestored, result.Status)

	target := filepath.Join(cwd, ".gitrieve", "github.com", "acme", "demo", "code")
	require.Equal(t, target, result.Target)
	config, err := os.ReadFile(filepath.Join(target, ".git", "config"))
	require.NoError(t, err)
	require.NotEmpty(t, config)
	source, err := os.ReadFile(filepath.Join(target, "main.go"))
	require.NoError(t, err)
	require.Equal(t, "package main\n", string(source))
}

func TestRestoreComponentDoesNotOverwriteExistingValidCache(t *testing.T) {
	base := t.TempDir()
	cwd := filepath.Join(base, "app")
	storageRoot := filepath.Join(base, "repo")
	target := filepath.Join(cwd, ".gitrieve", "github.com", "acme", "demo", "code")
	_, err := git.PlainInit(target, false)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(target, ".git", "sentinel"), []byte("existing\n"), 0o644))

	writeTarGz(t, filepath.Join(storageRoot, "github.com", "acme", "demo", "demo.tar.gz"), []archiveEntry{
		{name: "demo/.git/HEAD", body: "backup\n"},
	})

	r, err := scm.NewRepository("github.com/acme/demo")
	require.NoError(t, err)

	result, err := RestoreComponent(
		context.Background(), cwd, r, ComponentCode, fileStorages(storageRoot),
	)
	require.NoError(t, err)
	require.Equal(t, StatusAlreadyPresent, result.Status)

	head, err := os.ReadFile(filepath.Join(target, ".git", "sentinel"))
	require.NoError(t, err)
	require.Equal(t, "existing\n", string(head))
}

func TestRestoreComponentLeavesNoCacheWhenArchiveIsCorrupt(t *testing.T) {
	base := t.TempDir()
	cwd := filepath.Join(base, "app")
	storageRoot := filepath.Join(base, "repo")
	archivePath := filepath.Join(storageRoot, "github.com", "acme", "demo", "demo.tar.gz")
	require.NoError(t, os.MkdirAll(filepath.Dir(archivePath), 0o755))
	require.NoError(t, os.WriteFile(archivePath, []byte("not a gzip archive"), 0o644))

	r, err := scm.NewRepository("github.com/acme/demo")
	require.NoError(t, err)

	_, err = RestoreComponent(
		context.Background(), cwd, r, ComponentCode, fileStorages(storageRoot),
	)
	require.Error(t, err)

	target := filepath.Join(cwd, ".gitrieve", "github.com", "acme", "demo", "code")
	_, statErr := os.Stat(target)
	require.True(t, os.IsNotExist(statErr), "corrupt archive must not leave a partial cache")
}

func TestRestoreComponentRejectsArchivePathTraversal(t *testing.T) {
	base := t.TempDir()
	cwd := filepath.Join(base, "app")
	storageRoot := filepath.Join(base, "repo")
	require.NoError(t, os.MkdirAll(cwd, 0o755))

	writeTarGz(t, filepath.Join(storageRoot, "github.com", "acme", "demo", "demo.tar.gz"), []archiveEntry{
		{name: "demo/.git/HEAD", body: "ref: refs/heads/main\n"},
		{name: "demo/../../escape.txt", body: "escaped"},
	})

	r, err := scm.NewRepository("github.com/acme/demo")
	require.NoError(t, err)

	_, err = RestoreComponent(
		context.Background(), cwd, r, ComponentCode, fileStorages(storageRoot),
	)
	require.Error(t, err)
	require.Contains(t, err.Error(), "path traversal")

	target := filepath.Join(cwd, ".gitrieve", "github.com", "acme", "demo", "code")
	_, statErr := os.Stat(target)
	require.True(t, os.IsNotExist(statErr), "unsafe archive must not leave a partial cache")
	_, escapeErr := os.Stat(filepath.Join(base, "escape.txt"))
	require.True(t, os.IsNotExist(escapeErr), "traversal target must not be written")
}

func TestRestoreComponentReportsMissingArchive(t *testing.T) {
	base := t.TempDir()
	cwd := filepath.Join(base, "app")
	r, err := scm.NewRepository("github.com/acme/demo")
	require.NoError(t, err)

	result, err := RestoreComponent(
		context.Background(), cwd, r, ComponentCode, fileStorages(filepath.Join(base, "repo")),
	)
	require.NoError(t, err)
	require.Equal(t, StatusNotFound, result.Status)
}

func TestRestoreComponentRejectsChecksumMismatch(t *testing.T) {
	base := t.TempDir()
	cwd := filepath.Join(base, "app")
	storageRoot := filepath.Join(base, "repo")
	archivePath := filepath.Join(storageRoot, "github.com", "acme", "demo", "demo.tar.gz")
	writeTarGz(t, archivePath, []archiveEntry{
		{name: "demo/.git/HEAD", body: "ref: refs/heads/main\n"},
	})
	require.NoError(t, os.WriteFile(archivePath+".sha256", []byte(strings.Repeat("0", 64)+"\n"), 0o644))

	r, err := scm.NewRepository("github.com/acme/demo")
	require.NoError(t, err)
	_, err = RestoreComponent(context.Background(), cwd, r, ComponentCode, fileStorages(storageRoot))
	require.ErrorContains(t, err, "checksum mismatch")

	target := filepath.Join(cwd, ".gitrieve", "github.com", "acme", "demo", "code")
	_, statErr := os.Stat(target)
	require.True(t, os.IsNotExist(statErr))
}

func TestRestoreComponentAcceptsMarkedEmptyIssueCache(t *testing.T) {
	base := t.TempDir()
	cwd := filepath.Join(base, "app")
	target := filepath.Join(cwd, ".gitrieve", "github.com", "acme", "demo", "issues")
	require.NoError(t, MarkPresent(target, ComponentIssues))

	r, err := scm.NewRepository("github.com/acme/demo")
	require.NoError(t, err)
	result, err := RestoreComponent(
		context.Background(), cwd, r, ComponentIssues, fileStorages(filepath.Join(base, "repo")),
	)
	require.NoError(t, err)
	require.Equal(t, StatusAlreadyPresent, result.Status)
}

func TestComponentArchiveNamesAndTargets(t *testing.T) {
	r, err := scm.NewRepository("github.com/acme/demo")
	require.NoError(t, err)
	cwd := filepath.Join(t.TempDir(), "app")
	repoDir := filepath.Join(cwd, ".gitrieve", "github.com", "acme", "demo")

	tests := []struct {
		component Component
		archive   string
		target    string
	}{
		{ComponentCode, "demo.tar.gz", filepath.Join(repoDir, "code")},
		{ComponentWiki, "demo_wiki.tar.gz", filepath.Join(repoDir, "wiki")},
		{ComponentIssues, "issues.tar.gz", filepath.Join(repoDir, "issues")},
		{ComponentDiscussion, "discussions.tar.gz", filepath.Join(repoDir, "discussion")},
	}
	for _, tt := range tests {
		t.Run(string(tt.component), func(t *testing.T) {
			layout, err := LayoutFor(cwd, r, tt.component)
			require.NoError(t, err)
			require.Equal(t, tt.archive, layout.Archive)
			require.Equal(t, tt.target, layout.Target)
		})
	}
}
