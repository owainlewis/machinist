// Package artifacts stores immutable execution outputs independently of workers.
package artifacts

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

var ErrTooLarge = errors.New("artifact size limit exceeded")

type Store interface {
	Stage(io.Reader, int64) (*Pending, error)
	Publish(*Pending, string) error
	Open(string) (*os.File, error)
	Delete(string) error
}
type Filesystem struct{ directory string }
type Pending struct {
	Path, Checksum string
	Size           int64
}

func (p *Pending) Close() { _ = os.Remove(p.Path) }
func ValidPath(value string) bool {
	return value != "" && len(value) <= 1024 && value != "." && !strings.ContainsAny(value, "\\\x00\r\n") && !strings.HasPrefix(value, "/") && path.Clean(value) == value && value != ".." && !strings.HasPrefix(value, "../")
}
func NewFilesystem(directory string) (*Filesystem, error) {
	abs, err := filepath.Abs(directory)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(filepath.Join(abs, ".uploads"), 0700); err != nil {
		return nil, err
	}
	return &Filesystem{abs}, nil
}
func (s *Filesystem) Stage(reader io.Reader, limit int64) (pending *Pending, err error) {
	f, err := os.CreateTemp(filepath.Join(s.directory, ".uploads"), "upload-")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	defer func() {
		if err != nil {
			os.Remove(f.Name())
		}
	}()
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, hash), io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if n > limit {
		return nil, fmt.Errorf("%w: %d bytes", ErrTooLarge, limit)
	}
	if err = f.Sync(); err != nil {
		return nil, err
	}
	return &Pending{Path: f.Name(), Checksum: hex.EncodeToString(hash.Sum(nil)), Size: n}, nil
}
func (s *Filesystem) Publish(p *Pending, key string) error {
	if !ValidPath(key) {
		return errors.New("invalid artifact key")
	}
	target := filepath.Join(s.directory, filepath.FromSlash(key))
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		return err
	}
	if err := os.Link(p.Path, target); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	// Persist new directory entries all the way to the storage root.
	for parent := filepath.Dir(target); ; parent = filepath.Dir(parent) {
		dir, err := os.Open(parent)
		if err != nil {
			return err
		}
		err = dir.Sync()
		closeErr := dir.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		if parent == s.directory {
			break
		}
	}
	return nil
}
func (s *Filesystem) Open(key string) (*os.File, error) {
	if !ValidPath(key) {
		return nil, errors.New("invalid artifact key")
	}
	root, err := os.OpenRoot(s.directory)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return root.Open(key)
}
func (s *Filesystem) Delete(key string) error {
	if !ValidPath(key) {
		return errors.New("invalid artifact key")
	}
	root, err := os.OpenRoot(s.directory)
	if err != nil {
		return err
	}
	defer root.Close()
	err = root.Remove(key)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
