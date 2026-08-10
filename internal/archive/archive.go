package archive

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"

	"github.com/mholt/archives"
)

// Create packages the contents of the absolute path sourceDir into a gzip tarball,
// using targetName as the root directory inside the archive. It never mutates the
// process cwd, so it is safe to call concurrently from job goroutines.
//
// Note: sourceDir must be an absolute path, otherwise the packaging result depends
// on the process's current directory.
// TODO: archives over a certain size should be written to a temp file before being
// compressed, to avoid holding an entire repository in memory (this TODO previously
// hung on the caller; it moves here along with the packaging logic).
func Create(ctx context.Context, sourceDir, targetName string) (*bytes.Buffer, error) {
	if !filepath.IsAbs(sourceDir) {
		return nil, fmt.Errorf("archive: sourceDir must be an absolute path, got %q", sourceDir)
	}

	// archives.FilesFromDisk computes in-archive names by trimming sourceDir off the
	// walked filenames; a sourceDir with mixed separators (e.g. path.Join applied to
	// a Windows cwd) breaks that prefix match and leaks the absolute path into entry
	// names, so normalize to native separators first. This is a no-op on Linux.
	sourceDir = filepath.FromSlash(sourceDir)

	files, err := archives.FilesFromDisk(ctx, &archives.FromDiskOptions{}, map[string]string{
		sourceDir: targetName,
	})
	if err != nil {
		return nil, err
	}

	buf := &bytes.Buffer{}
	format := archives.CompressedArchive{
		Compression: archives.Gz{},
		Archival:    archives.Tar{},
	}
	if err := format.Archive(ctx, buf, files); err != nil {
		return nil, err
	}
	return buf, nil
}
