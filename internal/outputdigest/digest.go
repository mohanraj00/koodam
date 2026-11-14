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

// Package outputdigest computes the digest of the output file of an Agent run. The run pod has a
// container that calls it when the run ends. The controller reads the result from the
// termination message of that container.
package outputdigest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
)

// The reasons why a run has no digest.
const (
	// ReasonNotFound tells that the output path has no file.
	ReasonNotFound = "NotFound"
	// ReasonNotRegularFile tells that the output path is a directory, a link or a device.
	ReasonNotRegularFile = "NotRegularFile"
	// ReasonReadError tells that the container cannot read the file.
	ReasonReadError = "ReadError"
)

// Result is the digest of the output file, or the reason why there is no digest. It never has
// the content of the file. The JSON form is the termination message of the digest container.
type Result struct {
	// SHA256 is the SHA-256 digest of the file, as 64 lower-case hex characters.
	SHA256 string `json:"sha256,omitempty"`
	// Size is the size of the file in bytes.
	Size int64 `json:"size,omitempty"`
	// Reason is one of the reasons of this package. It is empty if the result has a digest.
	Reason string `json:"reason,omitempty"`
}

var hexDigest = regexp.MustCompile(`^[0-9a-f]{64}$`)

// File computes the digest of the regular file at path. root is the mount path of the output
// volume. The read stays in root, and no part of the path below root can be a link. Thus a link
// cannot make the container read a file that is not the file of the agent container. If root is
// path, the volume mount is the file itself.
func File(root, path string) Result {
	const flags = os.O_RDONLY | syscall.O_NOFOLLOW | syscall.O_NONBLOCK
	var f *os.File
	var err error
	rel, relErr := filepath.Rel(root, path)
	switch {
	case root == path:
		f, err = os.OpenFile(path, flags, 0)
	case relErr != nil || !filepath.IsLocal(rel):
		return Result{Reason: ReasonNotFound}
	default:
		dir, derr := os.OpenRoot(root)
		if derr != nil {
			return Result{Reason: ReasonReadError}
		}
		defer func() { _ = dir.Close() }()
		parts := strings.Split(rel, string(filepath.Separator))
		for i := range parts {
			info, lerr := dir.Lstat(filepath.Join(parts[:i+1]...))
			switch {
			case errors.Is(lerr, fs.ErrNotExist):
				return Result{Reason: ReasonNotFound}
			case lerr != nil:
				return Result{Reason: ReasonReadError}
			case info.Mode()&fs.ModeSymlink != 0:
				return Result{Reason: ReasonNotRegularFile}
			}
		}
		// The open through the root refuses a link that a different process adds after the check.
		f, err = dir.OpenFile(rel, flags, 0)
	}
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return Result{Reason: ReasonNotFound}
	case errors.Is(err, syscall.ELOOP):
		return Result{Reason: ReasonNotRegularFile}
	case err != nil:
		return Result{Reason: ReasonReadError}
	}
	defer func() { _ = f.Close() }()
	if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() {
		return Result{Reason: ReasonNotRegularFile}
	}
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return Result{Reason: ReasonReadError}
	}
	return Result{SHA256: hex.EncodeToString(h.Sum(nil)), Size: n}
}

// Encode gives back the termination message of r.
func (r Result) Encode() []byte {
	b, _ := json.Marshal(r)
	return b
}

// Parse reads a termination message. The message comes from a pod, so Parse accepts only a
// digest of the correct form or one of the reasons of this package.
func Parse(msg string) (Result, bool) {
	var r Result
	if err := json.Unmarshal([]byte(msg), &r); err != nil {
		return Result{}, false
	}
	switch r.Reason {
	case "":
		return r, hexDigest.MatchString(r.SHA256) && r.Size >= 0
	case ReasonNotFound, ReasonNotRegularFile, ReasonReadError:
		return Result{Reason: r.Reason}, true
	}
	return Result{}, false
}
