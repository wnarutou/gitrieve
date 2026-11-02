package repository

import (
	"os"
	"path/filepath"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/osfs"
)

// The default ChrootOS rewrites absolute symlink targets. Its Readlink then
// returns the original target with a different length from Lstat, causing
// go-git to report a dirty worktree even immediately after a hard reset.
func newManagedWorktreeFS(root string) billy.Filesystem {
	return &managedWorktreeFS{Filesystem: osfs.New(root, osfs.WithBoundOS())}
}

type managedWorktreeFS struct {
	billy.Filesystem
}

func (fs *managedWorktreeFS) Chroot(dir string) (billy.Filesystem, error) {
	child, err := fs.Filesystem.Chroot(dir)
	if err != nil {
		return nil, err
	}
	// BoundOS.Chroot returns the default ChrootOS; keep these semantics when
	// go-git opens a submodule's filesystem too.
	return newManagedWorktreeFS(child.Root()), nil
}

func (fs *managedWorktreeFS) Remove(name string) error {
	name = filepath.FromSlash(name)
	if !filepath.IsLocal(name) {
		return &os.PathError{Op: "remove", Path: name, Err: os.ErrPermission}
	}
	// BoundOS.Lstat validates the resolved parent directory without following
	// the final link. BoundOS.Remove follows that link, however: it can fail
	// on a dangling legacy link, or remove another tracked file it points at.
	info, err := fs.Filesystem.Lstat(name)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return os.Remove(filepath.Join(fs.Root(), name))
	}
	return fs.Filesystem.Remove(name)
}
