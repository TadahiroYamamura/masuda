// Package sharedfs writes into a directory tree the sandbox can also write:
// the workspace's state directory, shared into the VM over virtiofs.
// Anything below its root may have been swapped for a symlink from inside
// the sandbox, so every access goes through os.Root, which refuses to
// leave the root, and a whole file is replaced by rename, which swaps out
// a symlink at the target instead of writing through it. The root itself
// is the mount point on the guest side, which the sandbox cannot replace.
package sharedfs

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// WriteFile replaces root/name with data, creating the directories on the
// way.
func WriteFile(root, name string, data []byte) error {
	r, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer r.Close()
	dir := filepath.Dir(name)
	if err := r.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	var suffix [8]byte
	_, _ = rand.Read(suffix[:])
	tmp := filepath.Join(dir, ".tmp-"+hex.EncodeToString(suffix[:]))
	f, err := r.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		_ = r.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = r.Remove(tmp)
		return err
	}
	if err := r.Rename(tmp, name); err != nil {
		_ = r.Remove(tmp)
		return err
	}
	return nil
}

// OpenAppend opens root/name for appending, creating it if needed.
func OpenAppend(root, name string) (*os.File, error) {
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	if err := r.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		return nil, err
	}
	return r.OpenFile(name, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
}

// ReadRegular reads root/name, which must be a regular file rather than a
// symlink to one.
func ReadRegular(root, name string) ([]byte, error) {
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	st, err := r.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, &fs.PathError{Op: "read", Path: name, Err: errNotRegular}
	}
	return r.ReadFile(name)
}

var errNotRegular = errors.New("not a regular file")

// Remove deletes root/name, a symlink itself rather than what it points
// at. A name that does not exist is not an error.
func Remove(root, name string) error {
	r, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer r.Close()
	if err := r.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// RemoveAll deletes root/name and everything below it, without following
// symlinks out of the root.
func RemoveAll(root, name string) error {
	r, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer r.Close()
	return r.RemoveAll(name)
}
