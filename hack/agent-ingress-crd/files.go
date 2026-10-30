/*
Copyright 2026 Mohan Raj.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
)

// rename and write are the file operations that can fail during a write. Tests replace them.
var (
	rename = os.Rename
	write  = (*os.File).Write
)

// file is one target of writeAll. tmp is the new copy. orig is the copy of the original, or "" if
// the target did not exist.
type file struct {
	path      string
	data      []byte
	tmp, orig string
}

// writeAll replaces each target with its data, in order. First it writes and closes a full new copy
// and a copy of the original of each target, in the directory of the target. A failure up to this
// point changes no target. Then it renames each new copy over its target. If a rename fails, it
// puts back the targets that it already replaced. Two renames are not one atomic step. If the
// process stops between them, or if the put back fails, the copies of the originals stay next to
// the targets as .NAME.orig-*.
func writeAll(files ...*file) error {
	keep := false
	defer func() {
		for _, f := range files {
			remove(f.tmp)
			if !keep {
				remove(f.orig)
			}
		}
	}()
	for _, f := range files {
		if err := stage(f); err != nil {
			return err
		}
	}
	for i, f := range files {
		if err := rename(f.tmp, f.path); err != nil {
			if rerr := rollback(files[:i]); rerr != nil {
				keep = true
				var origs []string
				for _, f := range files {
					if f.orig != "" {
						origs = append(origs, f.orig)
					}
				}
				return fmt.Errorf("%w. The put back also failed: %w. The originals are in %q", err, rerr, origs)
			}
			return err
		}
		f.tmp = ""
	}
	return nil
}

func stage(f *file) error {
	old, err := os.ReadFile(f.path)
	if err == nil {
		if f.orig, err = temp(f.path, "orig", old); err != nil {
			return err
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	f.tmp, err = temp(f.path, "new", f.data)
	return err
}

// temp writes data to a new file next to path. The CRDs are public, so the mode is 0644.
func temp(path, kind string, data []byte) (string, error) {
	t, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"."+kind+"-*")
	if err != nil {
		return "", err
	}
	n, err := write(t, data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	if err == nil {
		err = t.Chmod(0o644)
	}
	if err == nil {
		err = t.Sync()
	}
	if cerr := t.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		remove(t.Name())
		return "", err
	}
	return t.Name(), nil
}

// rollback puts back the originals of the replaced targets, the last first.
func rollback(done []*file) error {
	var errs []error
	for _, f := range slices.Backward(done) {
		if f.orig == "" {
			errs = append(errs, os.Remove(f.path))
		} else if err := rename(f.orig, f.path); err != nil {
			errs = append(errs, err)
		} else {
			f.orig = ""
		}
	}
	return errors.Join(errs...)
}

// sameFile reports whether a and b can name one file. If it cannot tell, it reports true.
func sameFile(a, b string) bool {
	pa, errA := filepath.Abs(a)
	pb, errB := filepath.Abs(b)
	if errA != nil || errB != nil || pa == pb {
		return true
	}
	sa, errA := os.Stat(a)
	sb, errB := os.Stat(b)
	return errA == nil && errB == nil && os.SameFile(sa, sb)
}

// remove deletes a temporary file. A failure only leaves an extra hidden file.
func remove(name string) {
	if name != "" {
		_ = os.Remove(name)
	}
}
