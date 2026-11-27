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

package controller

import (
	"os/exec"
	"testing"
)

// TestNookkuLauncherChildPath runs the launcher test of the Nookku data image (#478). The child
// processes of the launcher must import the pinned lib, also with a nookku package in the
// current folder.
func TestNookkuLauncherChildPath(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("the launcher test needs python3")
	}
	out, err := exec.Command("sh", "../../images/nookku/test/launcher_test.sh").CombinedOutput()
	if err != nil {
		t.Fatalf("the Nookku launcher checks failed: %v\n%s", err, out)
	}
	t.Logf("%s", out)
}
