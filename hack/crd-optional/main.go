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

// Command crd-optional makes the containers list optional in the pod template of a CRD. The
// make target manifests runs it after controller-gen. controller-gen copies the rule
// "required: [containers]" from corev1.PodSpec. In a Koodam pod template the user can leave out
// the containers, because Koodam adds the main container.
package main

import (
	"fmt"
	"os"
	"strings"
)

func main() {
	for _, path := range os.Args[1:] {
		if err := run(path); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
}

func run(path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	out, n := patch(string(b))
	if n != 1 {
		return fmt.Errorf("%s: found %d pod template specs that require containers, want 1", path, n)
	}
	return os.WriteFile(path, []byte(out), 0o644) //nolint:gosec // The CRD is a public file.
}

// patch removes each "required:" list that has only the item "containers". It returns the new
// text and the number of removed lists.
func patch(s string) (string, int) {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	n := 0
	for i := 0; i < len(lines); i++ {
		indent := lines[i][:len(lines[i])-len(strings.TrimLeft(lines[i], " "))]
		if lines[i] == indent+"required:" && i+1 < len(lines) && lines[i+1] == indent+"- containers" &&
			(i+2 >= len(lines) || !strings.HasPrefix(lines[i+2], indent+"- ")) {
			i++
			n++
			continue
		}
		out = append(out, lines[i])
	}
	return strings.Join(out, "\n"), n
}
