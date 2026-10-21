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

package harness

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/mohanraj00/koodam/images/workspace"
)

// projectScript is the name of the koodam-project script in the harness folder.
const projectScript = "koodam-project"

// realName is the name of the harness binary in the harness folder. The usual name is the wrapper.
func realName(bin string) string { return "." + bin + "-real" }

// wrap writes the koodam-project script and a wrapper with the name bin into dir. The wrapper runs
// koodam-project in the current folder, then replaces itself with the real binary (exec). So the
// arguments, the environment, stdin, the exit code and the signals stay the same. The script
// always exits 0, so it never stops the harness. The agent image must have /bin/sh.
func wrap(dir, bin string) error {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	if err := writeExec(filepath.Join(abs, projectScript), workspace.ProjectScript); err != nil {
		return err
	}
	w := "#!/bin/sh\n# Koodam: make the project files for the harness, then run the harness.\n" +
		quote(filepath.Join(abs, projectScript)) + " \"$PWD\"\n" +
		"exec " + quote(filepath.Join(abs, realName(bin))) + " \"$@\"\n"
	return writeExec(filepath.Join(abs, bin), []byte(w))
}

// writeExec writes b to a temporary file, makes it executable and renames it to dst.
func writeExec(dst string, b []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".write-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	_, err = tmp.Write(b)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp.Name(), 0o755)
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp.Name(), dst)
}

// quote returns s in single quotes for sh.
func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
