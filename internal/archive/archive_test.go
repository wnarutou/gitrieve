package archive

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"sync"
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

// TestCreateArchiveConcurrentIsolation 从多个 goroutine 同时 Create，断言每个
// 归档只含自己的 sentinel 内容、且进程 cwd 未被改动。这是旧 os.Chdir 实现的
// 回归护栏：旧实现并发时相互踩踏 cwd，归档会串到别的仓库目录。
func TestCreateArchiveConcurrentIsolation(t *testing.T) {
	base := t.TempDir()
	const n = 12

	cwdBefore, err := os.Getwd()
	require.NoError(t, err)

	dirs := make([]string, n)
	for i := 0; i < n; i++ {
		src := path.Join(base, fmt.Sprintf("repo%d", i), "code")
		require.NoError(t, os.MkdirAll(src, 0o755))
		require.NoError(t, os.WriteFile(path.Join(src, "file.txt"), []byte(fmt.Sprintf("sentinel-%d", i)), 0o644))
		dirs[i] = src
	}

	start := make(chan struct{})
	bufs := make([]*bytes.Buffer, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			bufs[i], errs[i] = Create(context.Background(), dirs[i], "code")
		}(i)
	}
	close(start)
	wg.Wait()

	for i := 0; i < n; i++ {
		require.NoError(t, errs[i])
		want := map[string]string{"code/file.txt": fmt.Sprintf("sentinel-%d", i)}
		assert.Equal(t, want, extract(t, bufs[i]), "archive %d must contain exactly its own sentinel", i)
	}

	cwdAfter, err := os.Getwd()
	require.NoError(t, err)
	assert.Equal(t, cwdBefore, cwdAfter, "Create must never change the process cwd")
}

// TestCreateRejectsRelativeSourceDir guards the documented precondition that
// sourceDir must be absolute — a relative path would silently reintroduce
// process-cwd dependence, which is exactly what this package exists to prevent.
func TestCreateRejectsRelativeSourceDir(t *testing.T) {
	_, err := Create(context.Background(), "relative/path", "target")
	require.Error(t, err)
}
