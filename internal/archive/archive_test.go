package archive

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"os"
	"path"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// extract unpacks a gzip tarball into a map of "regular file archive name -> content",
// skipping directory entries.
func extract(t *testing.T, buf *bytes.Buffer) map[string]string {
	t.Helper()
	gzr, err := gzip.NewReader(bytes.NewReader(buf.Bytes()))
	require.NoError(t, err)
	defer gzr.Close()

	tr := tar.NewReader(gzr)
	contents := make(map[string]string)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		data, err := io.ReadAll(tr)
		require.NoError(t, err)
		contents[hdr.Name] = string(data)
	}
	return contents
}

// TestCreateArchiveEntryNamesMatchRelativeLayout locks in that archiving an absolute
// source directory produces the same entry layout as the old relative-path form
// (<target>/<rel>), guarding against archives upgrades breaking compatibility.
func TestCreateArchiveEntryNamesMatchRelativeLayout(t *testing.T) {
	src := path.Join(t.TempDir(), "tree")
	require.NoError(t, os.MkdirAll(path.Join(src, "sub"), 0o755))
	require.NoError(t, os.WriteFile(path.Join(src, "top.txt"), []byte("top"), 0o644))
	require.NoError(t, os.WriteFile(path.Join(src, "sub", "inner.txt"), []byte("inner"), 0o644))

	buf, err := Create(context.Background(), src, "target")
	require.NoError(t, err)

	files := extract(t, buf)
	assert.Equal(t, "top", files["target/top.txt"])
	assert.Equal(t, "inner", files["target/sub/inner.txt"])
}
