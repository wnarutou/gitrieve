package archive

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// extract unpacks a gzip tarball into a map of "regular file archive name -> content",
// skipping directory entries.
func extract(t *testing.T, packed io.Reader) map[string]string {
	t.Helper()
	gzr, err := gzip.NewReader(packed)
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
	t.Cleanup(func() { require.NoError(t, buf.Close()) })

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
	bufs := make([]*File, n)
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
	for _, packed := range bufs {
		if packed != nil {
			t.Cleanup(func() { require.NoError(t, packed.Close()) })
		}
	}

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

func TestCreateTrailingSeparatorKeepsStagingOutsideSource(t *testing.T) {
	parent := t.TempDir()
	src := filepath.Join(parent, "tree")
	require.NoError(t, os.Mkdir(src, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(src, "keep.txt"), []byte("keep"), 0o644))
	packed, err := Create(context.Background(), src+string(filepath.Separator), "target")
	require.NoError(t, err)
	defer packed.Close()
	require.Equal(t, parent, filepath.Dir(packed.Name()))
	require.Equal(t, map[string]string{"target/keep.txt": "keep"}, extract(t, packed))
}

func TestCreateLargeArchiveDoesNotBufferPayload(t *testing.T) {
	src := filepath.Join(t.TempDir(), "tree")
	require.NoError(t, os.Mkdir(src, 0o755))
	file, err := os.Create(filepath.Join(src, "large.bin"))
	require.NoError(t, err)
	const size = 32 << 20
	want := sha256.New()
	_, err = io.CopyN(io.MultiWriter(file, want), rand.New(rand.NewSource(1)), size)
	require.NoError(t, err)
	require.NoError(t, file.Close())

	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	packed, err := Create(context.Background(), src, "target")
	runtime.ReadMemStats(&after)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, packed.Close()) })
	// A payload-sized bytes.Buffer would exceed this budget even without its
	// growth copies. gzip, tar and the streaming copy need only a small window.
	require.Less(t, after.TotalAlloc-before.TotalAlloc, uint64(16<<20))
	info, err := packed.Stat()
	require.NoError(t, err)
	require.Greater(t, info.Size(), int64(31<<20), "fixture must remain largely incompressible")
	gz, err := gzip.NewReader(packed)
	require.NoError(t, err)
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		require.NoError(t, err)
		if header.Name != "target/large.bin" {
			continue
		}
		got := sha256.New()
		n, err := io.Copy(got, tr)
		require.NoError(t, err)
		require.Equal(t, int64(size), n)
		require.Equal(t, want.Sum(nil), got.Sum(nil))
		break
	}
}

func TestCreateFailureRemovesTemporaryArchive(t *testing.T) {
	parent := t.TempDir()
	packed, err := Create(context.Background(), filepath.Join(parent, "missing"), "target")
	require.Error(t, err)
	require.Nil(t, packed)
	entries, err := os.ReadDir(parent)
	require.NoError(t, err)
	require.Empty(t, entries)
}

func TestArchiveCloseRemovesOnlyItsTemporaryFile(t *testing.T) {
	parent := t.TempDir()
	src := filepath.Join(parent, "source")
	require.NoError(t, os.Mkdir(src, 0o755))
	source := filepath.Join(src, "keep.txt")
	require.NoError(t, os.WriteFile(source, []byte("keep"), 0o644))
	packed, err := Create(context.Background(), src, "target")
	require.NoError(t, err)
	name := packed.Name()
	require.Equal(t, parent, filepath.Dir(name), "temporary archive must be outside the source tree")
	require.NoError(t, packed.Close())
	_, err = os.Stat(name)
	require.True(t, os.IsNotExist(err))
	data, err := os.ReadFile(source)
	require.NoError(t, err)
	require.Equal(t, "keep", string(data))
}

func TestCreatePreservesSymlinkTargetWithoutFollowingIt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires permission to create Unix symlinks")
	}
	src := filepath.Join(t.TempDir(), "tree")
	require.NoError(t, os.Mkdir(src, 0o755))
	const target = "/nonexistent/external/target"
	require.NoError(t, os.Symlink(target, filepath.Join(src, "link")))
	packed, err := Create(context.Background(), src, "target")
	require.NoError(t, err)
	defer packed.Close()
	gz, err := gzip.NewReader(packed)
	require.NoError(t, err)
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		require.NoError(t, err)
		if header.Name == "target/link" {
			require.EqualValues(t, tar.TypeSymlink, header.Typeflag)
			require.Equal(t, target, header.Linkname)
			return
		}
	}
}
