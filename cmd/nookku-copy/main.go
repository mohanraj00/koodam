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

// Command nookku-copy is the init container of the Nookku data image (#83). It copies the pinned
// Nookku files of the image into the volumes of the pod. The arguments are pairs of a source and a
// destination. A source folder is copied into its destination folder, and a source file is copied
// to its destination path. A copied file has the mode 0444, or 0555 if the source can run. The
// command refuses a link or a special file. It reads no env and opens no network connection.
package main

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "nookku-copy:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 || len(args)%2 != 0 {
		return fmt.Errorf("give pairs of a source and a destination")
	}
	for i := 0; i < len(args); i += 2 {
		src, dst := args[i], args[i+1]
		info, err := os.Lstat(src)
		if err != nil {
			return err
		}
		if !info.IsDir() {
			if err := copyFile(src, dst, info); err != nil {
				return err
			}
			continue
		}
		err = filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(src, path)
			if err != nil {
				return err
			}
			target := filepath.Join(dst, rel)
			if d.IsDir() {
				return os.MkdirAll(target, 0o755)
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			return copyFile(path, target, info)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// copyFile copies one regular file. The destination must not exist.
func copyFile(src, dst string, info fs.FileInfo) error {
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", src)
	}
	mode := os.FileMode(0o444)
	if info.Mode()&0o111 != 0 {
		mode = 0o555
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
