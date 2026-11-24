package storage

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPutObjectWithChecksumWritesSidecar(t *testing.T) {
	path := filepath.Join(t.TempDir(), "archive.tar.gz")
	backend := &File{}

	require.NoError(t, PutObjectReaderWithChecksum(
		context.Background(), backend, path, bytes.NewReader([]byte("hello")),
	))
	require.FileExists(t, path)
	sidecar, err := os.ReadFile(path + ".sha256")
	require.NoError(t, err)
	require.Equal(t, "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824\n", string(sidecar))
}
