package storage

import (
	"errors"
	"time"

	"github.com/wnarutou/gitrieve/internal/typedef"
)

const (
	FileStorage = "file"
	S3Storage   = "s3"
)

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
	// DeleteObject deletes the object identified by the given identifier.
	DeleteObject(identifier string) error
}

func GetStorage(storage typedef.MultiStorage) (Storage, error) {
	var (
		backend Storage
		err     error
	)
	switch storage.Type {
	case FileStorage:
		backend = &File{}
	case S3Storage:
		backend, err = New(storage.Endpoint, storage.Bucket, storage.Region, storage.AccessKeyID, storage.SecretAccessKey)
	default:
		err = errors.New("unknown storage type")
	}
	return backend, err
}
