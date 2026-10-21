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

package chart

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	"sigs.k8s.io/yaml"
)

// managerArgs returns the args of the manager container.
func managerArgs(t *testing.T, out string) []string {
	t.Helper()
	for _, doc := range objects(t, out, "Deployment") {
		var d appsv1.Deployment
		if err := yaml.Unmarshal([]byte(doc), &d); err != nil {
			t.Fatal(err)
		}
		for _, c := range d.Spec.Template.Spec.Containers {
			if c.Name == "manager" {
				return c.Args
			}
		}
	}
	t.Fatal("the chart has no manager container")
	return nil
}

// appVersion returns the appVersion in Chart.yaml.
func appVersion(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(chartDir, "Chart.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var chart struct {
		AppVersion string `json:"appVersion"`
	}
	if err := yaml.Unmarshal(data, &chart); err != nil {
		t.Fatal(err)
	}
	return chart.AppVersion
}

// workspaceImageArg returns the value of the flag --workspace-image.
func workspaceImageArg(args []string) (string, bool) {
	for _, a := range args {
		if v, ok := strings.CutPrefix(a, "--workspace-image="); ok {
			return v, true
		}
	}
	return "", false
}

func TestWorkspaceImage(t *testing.T) {
	cases := map[string]struct {
		set  []string
		want string
	}{
		// The default tag is the chart appVersion, the same tag as the controller image.
		"default": {
			want: "ghcr.io/mohanraj00/koodam-workspace:" + appVersion(t),
		},
		"tag": {
			set:  []string{"workspace.image.tag=0.2.0"},
			want: "ghcr.io/mohanraj00/koodam-workspace:0.2.0",
		},
		"repository and tag": {
			set:  []string{"workspace.image.repository=registry.example/ws", "workspace.image.tag=dev"},
			want: "registry.example/ws:dev",
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			out, err := helmTemplate(t, "", c.set...)
			if err != nil {
				t.Fatalf("helm template failed: %v\n%s", err, out)
			}
			got, ok := workspaceImageArg(managerArgs(t, out))
			if !ok {
				t.Fatal("the manager has no --workspace-image flag")
			}
			if got != c.want {
				t.Errorf("--workspace-image is %q, not %q", got, c.want)
			}
		})
	}
}

func TestWorkspaceImageRefusesEmptyRepository(t *testing.T) {
	out, err := helmTemplate(t, "", "workspace.image.repository=")
	if err == nil {
		t.Fatal("helm template must fail")
	}
	if !strings.Contains(out, "workspace.image.repository is necessary.") {
		t.Errorf("the error does not give the value name:\n%s", out)
	}
}
