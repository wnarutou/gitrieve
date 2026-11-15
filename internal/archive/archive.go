package archive

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
)

// File is a completed, seekable archive on disk. Close releases the handle and
// removes only this temporary artifact, never the source or a published archive.
type File struct{ *os.File }

func (f *File) Close() error {
	return errors.Join(f.File.Close(), os.Remove(f.Name()))
}

// Create streams a gzip tarball into a temporary file next to sourceDir, outside
// the archived tree and on the same mounted cache volume. The caller must Close
// it after publishing to every destination. It never changes the process cwd.
func Create(ctx context.Context, sourceDir, targetName string) (_ *File, resultErr error) {
	if !filepath.IsAbs(sourceDir) {
		return nil, fmt.Errorf("archive: sourceDir must be an absolute path, got %q", sourceDir)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sourceDir = filepath.Clean(filepath.FromSlash(sourceDir))
	if filepath.Dir(sourceDir) == sourceDir {
		return nil, fmt.Errorf("archive: cannot stage an archive outside filesystem root %q", sourceDir)
	}
	temp, err := os.CreateTemp(filepath.Dir(sourceDir), ".gitrieve-archive-*.tar.gz")
	if err != nil {
		return nil, err
	}
	packed := &File{File: temp}
	complete := false
	defer func() {
		if !complete {
			resultErr = errors.Join(resultErr, packed.Close())
		}
	}()
	if err := writeArchive(ctx, temp, sourceDir, targetName); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := temp.Sync(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, err := temp.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	complete = true
	return packed, nil
}

// Walk one entry at a time instead of retaining a manifest for the entire tree.
// File payloads pass through one reusable buffer and gzip's compression window.
func writeArchive(ctx context.Context, dst io.Writer, sourceDir, targetName string) (resultErr error) {
	gz := gzip.NewWriter(dst)
	defer func() { resultErr = errors.Join(resultErr, gz.Close()) }()
	tw := tar.NewWriter(gz)
	defer func() { resultErr = errors.Join(resultErr, tw.Close()) }()
	buffer := make([]byte, 32*1024)
	return filepath.WalkDir(sourceDir, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		var link string
		if info.Mode()&os.ModeSymlink != 0 {
			link, err = os.Readlink(name)
			if err != nil {
				return err
			}
		}
		header, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(sourceDir, name)
		if err != nil {
			return err
		}
		header.Name = path.Join(targetName, filepath.ToSlash(relative))
		if info.IsDir() {
			header.Name += "/"
		}
		if err := tw.WriteHeader(header); err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		file, err := os.Open(name)
		if err != nil {
			return err
		}
		_, copyErr := io.CopyBuffer(tw, contextReader{ctx, file}, buffer)
		return errors.Join(copyErr, file.Close())
	})
}

type contextReader struct {
	ctx context.Context
	io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.Reader.Read(p)
}
