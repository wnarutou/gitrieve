package storage

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

type failedReader struct{ err error }

func (r failedReader) Read([]byte) (int, error) { return 0, r.err }

func TestPutObjectReaderPreservesPreviousArchiveOnReadFailure(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "repo.tar.gz")
	require.NoError(t, os.WriteFile(target, []byte("previous archive"), 0o600))
	failure := errors.New("source failed after partial data")
	reader := io.MultiReader(bytes.NewBufferString("partial replacement"), failedReader{failure})
	err := (File{}).PutObjectReader(context.Background(), target, reader)
	require.ErrorIs(t, err, failure)
	data, err := os.ReadFile(target)
	require.NoError(t, err)
	require.Equal(t, "previous archive", string(data))
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1, "a failed publication must remove its staging file")
}

type cancellingReader struct{ cancel context.CancelFunc }

func (r cancellingReader) Read(p []byte) (int, error) {
	r.cancel()
	return copy(p, "partial data"), nil
}

func TestPutObjectReaderCancellationPreservesPreviousArchive(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "repo.tar.gz")
	require.NoError(t, os.WriteFile(target, []byte("previous archive"), 0o600))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := (File{}).PutObjectReader(ctx, target, cancellingReader{cancel})
	require.ErrorIs(t, err, context.Canceled)
	data, err := os.ReadFile(target)
	require.NoError(t, err)
	require.Equal(t, "previous archive", string(data))
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
}

func TestPutObjectReaderPublishesOnlyAfterSuccessfulRead(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "repo.tar.gz")
	require.NoError(t, os.WriteFile(target, []byte("previous archive"), 0o600))
	reader := &observeOldArchiveReader{t: t, target: target, Reader: bytes.NewBufferString("complete replacement")}
	require.NoError(t, (File{}).PutObjectReader(context.Background(), target, reader))
	data, err := os.ReadFile(target)
	require.NoError(t, err)
	require.Equal(t, "complete replacement", string(data))
	info, err := os.Stat(target)
	require.NoError(t, err)
	if runtime.GOOS != "windows" {
		require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	}
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
}

type observeOldArchiveReader struct {
	t      *testing.T
	target string
	io.Reader
}

func (r *observeOldArchiveReader) Read(p []byte) (int, error) {
	data, err := os.ReadFile(r.target)
	require.NoError(r.t, err)
	require.Equal(r.t, "previous archive", string(data), "destination must remain intact while streaming")
	if runtime.GOOS != "windows" {
		entries, err := os.ReadDir(filepath.Dir(r.target))
		require.NoError(r.t, err)
		for _, entry := range entries {
			if entry.Name() == filepath.Base(r.target) {
				continue
			}
			info, err := entry.Info()
			require.NoError(r.t, err)
			require.Equal(r.t, os.FileMode(0o600), info.Mode().Perm(), "private archive permissions must hold during staging too")
		}
	}
	return r.Reader.Read(p)
}

func TestPutObjectReaderDoesNotDeleteDestinationWhenRenameFails(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "repo.tar.gz")
	require.NoError(t, os.Mkdir(target, 0o755))
	marker := filepath.Join(target, "keep")
	require.NoError(t, os.WriteFile(marker, []byte("keep"), 0o600))
	require.Error(t, (File{}).PutObjectReader(context.Background(), target, bytes.NewBufferString("replacement")))
	data, err := os.ReadFile(marker)
	require.NoError(t, err)
	require.Equal(t, "keep", string(data))
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
}
