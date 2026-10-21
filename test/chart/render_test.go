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

// Package chart has the render tests for the Helm chart in charts/koodam.
// The tests run "helm template". If helm is not on the PATH, the tests stop,
// but in CI (the variable CI is set) the tests fail.
package chart

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/yaml"
)

const chartDir = "../../charts/koodam"

// upstreamValues gives an upstream key to LiteLLM from a Secret.
// The values have no key value: the model reads the key from the env.
const upstreamValues = `
gateway:
  bundled:
    litellm:
      modelList:
      - model_name: claude
        litellm_params:
          model: anthropic/claude-sonnet-4-5
          api_key: os.environ/ANTHROPIC_API_KEY
      extraEnvFromSecrets:
      - name: ANTHROPIC_API_KEY
        secretName: upstream-keys
        secretKey: anthropic
      - name: OPENAI_API_KEY
        secretName: upstream-keys
        secretKey: openai
`

func helmTemplate(t *testing.T, values string, set ...string) (string, error) {
	t.Helper()
	helm, err := exec.LookPath("helm")
	if err != nil {
		if os.Getenv("CI") != "" {
			t.Fatal("helm is not on the PATH")
		}
		t.Skip("helm is not on the PATH")
	}
	file := filepath.Join(t.TempDir(), "values.yaml")
	if err := os.WriteFile(file, []byte(values), 0o600); err != nil {
		t.Fatal(err)
	}
	args := make([]string, 0, 9+2*len(set))
	args = append(args, "template", "koodam", chartDir, "--namespace", "koodam-system",
		"-f", filepath.Join(chartDir, "values-bundled.yaml"), "-f", file)
	for _, s := range set {
		args = append(args, "--set", s)
	}
	out, err := exec.Command(helm, args...).CombinedOutput()
	return string(out), err
}

// objects returns the rendered objects of one kind.
func objects(t *testing.T, out, kind string) []string {
	t.Helper()
	var docs []string
	for doc := range strings.SplitSeq(out, "\n---\n") {
		var meta struct{ Kind string }
		if err := yaml.Unmarshal([]byte(doc), &meta); err != nil {
			t.Fatalf("cannot parse a rendered object: %v", err)
		}
		if meta.Kind == kind {
			docs = append(docs, doc)
		}
	}
	return docs
}

func litellmObjects(t *testing.T, out string) (corev1.Container, string) {
	t.Helper()
	var container *corev1.Container
	for _, doc := range objects(t, out, "Deployment") {
		var d appsv1.Deployment
		if err := yaml.Unmarshal([]byte(doc), &d); err != nil {
			t.Fatal(err)
		}
		for i := range d.Spec.Template.Spec.Containers {
			if d.Spec.Template.Spec.Containers[i].Name == "litellm" {
				container = &d.Spec.Template.Spec.Containers[i]
			}
		}
	}
	if container == nil {
		t.Fatal("the chart has no litellm container")
	}
	var config string
	for _, doc := range objects(t, out, "ConfigMap") {
		var cm corev1.ConfigMap
		if err := yaml.Unmarshal([]byte(doc), &cm); err != nil {
			t.Fatal(err)
		}
		if c, ok := cm.Data["config.yaml"]; ok {
			config = c
		}
	}
	if config == "" {
		t.Fatal("the chart has no LiteLLM ConfigMap")
	}
	return *container, config
}

func TestUpstreamKeysComeFromSecrets(t *testing.T) {
	out, err := helmTemplate(t, upstreamValues)
	if err != nil {
		t.Fatalf("helm template failed: %v\n%s", err, out)
	}
	container, config := litellmObjects(t, out)

	want := map[string]string{"ANTHROPIC_API_KEY": "anthropic", "OPENAI_API_KEY": "openai"}
	for _, env := range container.Env {
		key, ok := want[env.Name]
		if !ok {
			continue
		}
		delete(want, env.Name)
		if env.Value != "" {
			t.Errorf("%s has a literal value", env.Name)
		}
		ref := env.ValueFrom
		if ref == nil || ref.SecretKeyRef == nil {
			t.Fatalf("%s does not use secretKeyRef", env.Name)
		}
		if ref.SecretKeyRef.Name != "upstream-keys" || ref.SecretKeyRef.Key != key {
			t.Errorf("%s refers to %s/%s, not upstream-keys/%s",
				env.Name, ref.SecretKeyRef.Name, ref.SecretKeyRef.Key, key)
		}
	}
	if len(want) != 0 {
		t.Errorf("the litellm container has no env for %v", want)
	}

	// The ConfigMap has only env references for the keys. The mock model uses "not-used".
	var parsed struct {
		ModelList []struct {
			LitellmParams map[string]any `json:"litellm_params"`
		} `json:"model_list"`
	}
	if err := yaml.Unmarshal([]byte(config), &parsed); err != nil {
		t.Fatal(err)
	}
	for _, m := range parsed.ModelList {
		key, _ := m.LitellmParams["api_key"].(string)
		if key != "not-used" && !strings.HasPrefix(key, "os.environ/") {
			t.Errorf("the ConfigMap has an api_key that is not an env reference")
		}
	}
	if !strings.Contains(config, "os.environ/ANTHROPIC_API_KEY") {
		t.Error("the ConfigMap does not have the model from modelList")
	}
}

func TestUpstreamKeysDefaultIsEmpty(t *testing.T) {
	out, err := helmTemplate(t, "")
	if err != nil {
		t.Fatalf("helm template failed: %v\n%s", err, out)
	}
	container, _ := litellmObjects(t, out)
	if len(container.Env) != 3 {
		t.Errorf("the litellm container has %d env entries, not 3", len(container.Env))
	}
}

func TestUpstreamKeysRefuseBadItems(t *testing.T) {
	item := "gateway.bundled.litellm.extraEnvFromSecrets[0]"
	cases := map[string]struct {
		set  []string
		want string
	}{
		"no secret key": {
			set:  []string{item + ".name=ANTHROPIC_API_KEY", item + ".secretName=upstream-keys"},
			want: item + ".secretKey is necessary.",
		},
		"no secret name": {
			set:  []string{item + ".name=ANTHROPIC_API_KEY", item + ".secretKey=anthropic"},
			want: item + ".secretName is necessary.",
		},
		"no name": {
			set:  []string{item + ".secretName=upstream-keys", item + ".secretKey=anthropic"},
			want: item + ".name is necessary.",
		},
		"a chart variable": {
			set: []string{item + ".name=LITELLM_MASTER_KEY", item + ".secretName=upstream-keys",
				item + ".secretKey=anthropic"},
			want: "must not be LITELLM_MASTER_KEY",
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			out, err := helmTemplate(t, "", c.set...)
			if err == nil {
				t.Fatal("helm template must fail")
			}
			if !strings.Contains(out, c.want) {
				t.Errorf("the error does not contain %q:\n%s", c.want, out)
			}
		})
	}
}
