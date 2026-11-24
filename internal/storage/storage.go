package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/wnarutou/gitrieve/internal/typedef"
)

const (
	FileStorage = "file"
)

// ErrObjectNotFound identifies a missing object so callers can distinguish an
// absent optional archive from a storage failure.
var ErrObjectNotFound = errors.New("object not found")

type ObjectMetaInfo struct {
	Path         string
	Size         int64
	LastModified time.Time
}
type Object struct {
	MetaInfo ObjectMetaInfo
	Content  []byte
}

type Storage interface {
	// ListObject returns a list of all objects in the storage backend.
	ListObject(prefix string) ([]Object, error)
	// ListObjectMetaInfo returns a list of all objects's meta info in the storage backend.
	ListObjectMetaInfo(prefix string) ([]ObjectMetaInfo, error)
	// GetObject returns the object identified by the given identifier.
	GetObject(identifier string) (Object, error)
	// PutObject stores the data in the storage backend identified by the given identifier.
	PutObject(identifier string, data []byte) error
	// PutObjectReader streams to a same-directory temporary file and replaces
	// the destination only after a complete, successful write. It does not
	// close data; callers must rewind seekable archives for each destination.
	PutObjectReader(ctx context.Context, identifier string, data io.Reader) error
	// DeleteObject deletes the object identified by the given identifier.
	DeleteObject(identifier string) error
}

// ObjectReader is the streaming read path used for large cache archives.
// Backends that implement it avoid loading a whole archive into memory.
type ObjectReader interface {
	OpenObject(identifier string) (io.ReadCloser, error)
}

// IsNotFound reports whether err identifies an absent object across backends.
func IsNotFound(err error) bool {
	return errors.Is(err, ErrObjectNotFound) || os.IsNotExist(err)
}

// PutObjectReaderWithChecksum streams a seekable archive to storage and writes
// its SHA-256 sidecar. The sidecar is optional on read so archives written by
// older versions remain restorable.
func PutObjectReaderWithChecksum(ctx context.Context, backend Storage, identifier string, data io.ReadSeeker) error {
	if _, err := data.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("rewind archive for checksum: %w", err)
	}
	hasher := sha256.New()
	if _, err := io.Copy(hasher, data); err != nil {
		return fmt.Errorf("checksum archive: %w", err)
	}
	if _, err := data.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("rewind archive for upload: %w", err)
	}
	if err := backend.PutObjectReader(ctx, identifier, data); err != nil {
		return err
	}
	sidecar := []byte(hex.EncodeToString(hasher.Sum(nil)) + "\n")
	if err := backend.PutObject(identifier+".sha256", sidecar); err != nil {
		return fmt.Errorf("write checksum for %s: %w", identifier, err)
	}
	return nil
}

func GetStorage(storage typedef.MultiStorage) (Storage, error) {
	if err := storage.ValidateType(); err != nil {
		return nil, err
	}
	return &File{}, nil
}
