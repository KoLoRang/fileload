package store

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

// jsonFile is one JSON document on disk, cached in memory after the first
// read so hot paths (UI snapshots) never touch the disk. Writes are atomic
// (temp file + rename) so a crash never leaves a half-written file.
type jsonFile[T any] struct {
	path string

	mu     sync.Mutex
	loaded bool
	val    T
}

func newJSONFile[T any](path string) *jsonFile[T] { return &jsonFile[T]{path: path} }

// get returns the cached value. A missing or corrupt file yields the zero
// value (corrupt files are kept on disk untouched until the next save).
func (f *jsonFile[T]) get() T {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.loadLocked()
	return f.val
}

// update applies fn to the current value and persists the result.
// The cached value only changes when the write succeeds.
func (f *jsonFile[T]) update(fn func(T) (T, error)) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.loadLocked()
	next, err := fn(f.val)
	if err != nil {
		return err
	}
	if err := writeAtomic(f.path, next); err != nil {
		return err
	}
	f.val = next
	return nil
}

func (f *jsonFile[T]) set(v T) error {
	return f.update(func(T) (T, error) { return v, nil })
}

func (f *jsonFile[T]) loadLocked() {
	if f.loaded {
		return
	}
	f.loaded = true
	data, err := os.ReadFile(f.path)
	if err != nil {
		return
	}
	var v T
	if json.Unmarshal(data, &v) == nil {
		f.val = v
	}
}

func writeAtomic(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if err := errors.Join(werr, cerr); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil && !errors.Is(err, fs.ErrPermission) {
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return nil
}
