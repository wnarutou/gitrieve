package storage

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/google/uuid"
)

var _ Storage = (*File)(nil)

type File struct {
}

func (f File) ListObjectMetaInfo(prefix string) ([]ObjectMetaInfo, error) {
	if prefix == "" {
		return nil, errors.New("invalid prefix: prefix cannot be empty")
	}

	// Clean the prefix to handle any relative or special path characters
	cleanedPrefix := filepath.Clean(prefix)

	// Check if the prefix is a directory
	info, err := os.Stat(cleanedPrefix)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, errors.New("invalid prefix: does not exist")
		}
		return nil, err
	}

	var objects []ObjectMetaInfo
	if info.IsDir() {
		// Read the contents
		entries, err := os.ReadDir(cleanedPrefix)
		if err != nil {
			return nil, err
		}

		for _, entry := range entries {
			filePath := filepath.Join(cleanedPrefix, entry.Name())
			fileInfo, err := os.Stat(filePath)
			if err != nil {
				return nil, err
			}

			objects = append(objects, ObjectMetaInfo{
				Path:         filepath.Join(prefix, entry.Name()),
				Size:         fileInfo.Size(),
				LastModified: fileInfo.ModTime(),
			})
		}
	} else {
		objects = append(objects, ObjectMetaInfo{
			Path:         info.Name(),
			Size:         info.Size(),
			LastModified: info.ModTime(),
		})
	}
	return objects, nil
}

func (f File) ListObject(prefix string) ([]Object, error) {
	// Get metadata info first
	metaInfos, err := f.ListObjectMetaInfo(prefix)
	if err != nil {
		return nil, err
	}

	var objects []Object
	for _, meta := range metaInfos {
		data, err := os.ReadFile(filepath.Clean(meta.Path))
		if err != nil {
			return nil, err
		}

		objects = append(objects, Object{
			Content:  data,
			MetaInfo: meta,
		})
	}

	return objects, nil
}

func (f File) GetObject(identifier string) (Object, error) {
	if identifier == "" {
		return Object{}, errors.New("invalid identifier: identifier cannot be empty")
	}

	filePath := filepath.Clean(identifier)
	info, err := os.Stat(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return Object{}, errors.New("invalid identifier: file does not exist")
		}
		return Object{}, err
	}

	if info.IsDir() {
		return Object{}, errors.New("invalid identifier: identifier points to a directory, not a file")
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		return Object{}, err
	}

	return Object{
		Content: data,
		MetaInfo: ObjectMetaInfo{
			Path:         identifier,
			Size:         info.Size(),
			LastModified: info.ModTime()},
	}, nil
}

func (f File) DeleteObject(identifier string) error {
	if identifier == "" {
		return errors.New("invalid identifier: identifier cannot be empty")
	}

	filePath := filepath.Clean(identifier)
	_, err := os.Stat(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return errors.New("invalid identifier: does not exist")
		}
		return err
	}

	// Proceed with the deletion
	err = os.Remove(filePath)
	if err != nil {
		return err
	}

	return nil
}

// CreateDirIfNotExist creates the directory for the given file path if it does not exist.
func CreateDirIfNotExist(dir string) error {
	_, err := os.Stat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			err := os.MkdirAll(dir, 0774)
			if err != nil {
				return err
			}
			// os.MkdirAll set the dir permissions before the umask
			// we need to use os.Chmod to ensure the permissions of the created directory are 774
			// because the default umask will prevent that and cause the permissions to be 755
			err = os.Chmod(dir, 0774)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func (f File) PutObject(identifier string, data []byte) error {
	return f.PutObjectReader(context.Background(), identifier, bytes.NewReader(data))
}

func (f File) PutObjectReader(ctx context.Context, identifier string, data io.Reader) (resultErr error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	dir := filepath.Dir(identifier)
	if err := CreateDirIfNotExist(dir); err != nil {
		return err
	}
	// Preserve existing permissions; new files retain PutObject's 0664 mode
	// filtered by the process umask, without changing that process-wide mask.
	var mode os.FileMode
	preserveMode := false
	if info, err := os.Stat(identifier); err == nil {
		mode = info.Mode().Perm()
		preserveMode = true
	} else if !os.IsNotExist(err) {
		return err
	}
	id, err := uuid.NewRandom()
	if err != nil {
		return err
	}
	name := filepath.Join(dir, ".gitrieve-write-"+id.String())
	creationMode := os.FileMode(0o664)
	if preserveMode {
		creationMode = mode
	}
	temp, err := os.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, creationMode)
	if err != nil {
		return err
	}
	defer func() {
		if temp != nil {
			resultErr = errors.Join(resultErr, temp.Close())
		}
		if err := os.Remove(name); err != nil && !os.IsNotExist(err) {
			resultErr = errors.Join(resultErr, err)
		}
	}()
	// Set preserved permissions before copying any payload, so staging a
	// private archive never exposes it with the broader default file mode.
	if preserveMode {
		if err := temp.Chmod(mode); err != nil {
			return err
		}
	}
	if _, err := io.CopyBuffer(temp, contextReader{ctx, data}, make([]byte, 32*1024)); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := temp.Sync(); err != nil {
		return err
	}
	closeErr := temp.Close()
	temp = nil
	if closeErr != nil {
		return closeErr
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// No remove-then-rename fallback: a failed replacement must leave the old
	// archive intact. Staging in this directory also avoids cross-device moves.
	return os.Rename(name, identifier)
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
