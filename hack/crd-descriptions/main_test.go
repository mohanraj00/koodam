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
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"sigs.k8s.io/yaml"
)

const (
	keep   = "koodam text"
	remove = "Kubernetes text"
)

// input returns a CRD with one pod template in each of 2 versions. Each schema has a description.
func input(t *testing.T) []byte {
	t.Helper()
	text := func(description string, properties map[string]schema) schema {
		return schema{Type: "object", Description: description, Properties: properties}
	}
	value := &schema{Type: "string", Description: remove}
	container := text(remove, map[string]schema{"env": {Type: "array", Description: remove,
		Items: &apiextensionsv1.JSONSchemaPropsOrArray{Schema: value.DeepCopy()}}})
	root := text(keep, map[string]schema{"spec": text(keep, map[string]schema{
		"model": {Type: "string", Description: keep},
		"podTemplate": text(keep, map[string]schema{
			"metadata": text(remove, map[string]schema{"labels": {Type: "object", Description: remove,
				AdditionalProperties: &apiextensionsv1.JSONSchemaPropsOrBool{Schema: value.DeepCopy()}}}),
			"spec": text(remove, map[string]schema{"containers": {Type: "array", Description: remove,
				Items: &apiextensionsv1.JSONSchemaPropsOrArray{Schema: &container}}}),
		}),
	})})
	crd := &crdFile{}
	crd.APIVersion, crd.Kind = "apiextensions.k8s.io/v1", "CustomResourceDefinition"
	crd.Metadata.Name = "agents.core.koodam.dev"
	for _, name := range []string{"v1alpha1", "v1beta1"} {
		crd.Spec.Versions = append(crd.Spec.Versions, apiextensionsv1.CustomResourceDefinitionVersion{Name: name,
			Schema: &apiextensionsv1.CustomResourceValidation{OpenAPIV3Schema: root.DeepCopy()}})
	}
	out, err := marshal(crd)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestPatchRemovesOnlyTheTextBelowAPodTemplate(t *testing.T) {
	in := input(t)
	out, err := patch(in)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), remove) || strings.Count(string(out), keep) != strings.Count(string(in), keep) {
		t.Errorf("want no %q and each %q:\n%s", remove, keep, out)
	}
	// The output is the input with no line of a removed description. Thus no validation changed.
	var lines []string
	for line := range strings.SplitSeq(string(in), "\n") {
		if !strings.Contains(line, remove) {
			lines = append(lines, line)
		}
	}
	if want := strings.Join(lines, "\n"); string(out) != want {
		t.Errorf("got:\n%s\nwant:\n%s", out, want)
	}
	again, err := patch(out)
	if err != nil || !bytes.Equal(again, out) {
		t.Errorf("a second run changed the output: %v", err)
	}
}

func TestPatchRefusals(t *testing.T) {
	in := string(input(t))
	for name, bad := range map[string]string{
		"not YAML":                       "{",
		"an unknown field":               strings.Replace(in, "kind:", "other: 1\nkind:", 1),
		"a second document":              in + in,
		"a version with no pod template": strings.Replace(in, "containers:", "others:", 1),
	} {
		if _, err := patch([]byte(bad)); err == nil {
			t.Errorf("%s: got no error", name)
		}
	}
}

// TestGeneratedCRDs makes sure that the CRDs in the repository have the koodam descriptions and
// no description below a pod template, and that the two versions have a pod template each.
func TestGeneratedCRDs(t *testing.T) {
	for _, path := range []string{"crd/bases/core.koodam.dev_agents.yaml", "crd/bases/core.koodam.dev_workspaces.yaml",
		"apps/crd/bases/core.koodam.dev_appversions.yaml", "agent-ingress/crd/bases/core.koodam.dev_agents.yaml"} {
		raw, err := os.ReadFile(filepath.Join("..", "..", "config", filepath.FromSlash(path)))
		if err != nil {
			t.Fatal(err)
		}
		crd := &crdFile{}
		if err := yaml.Unmarshal(raw, crd); err != nil {
			t.Fatal(err)
		}
		for _, v := range crd.Spec.Versions {
			root := v.Schema.OpenAPIV3Schema
			if n := clean(root.DeepCopy()); n != 1 {
				t.Errorf("%s %s: found %d pod templates, want 1", path, v.Name, n)
			}
			before, err := yaml.Marshal(root)
			if err != nil {
				t.Fatal(err)
			}
			clean(root)
			after, err := yaml.Marshal(root)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Errorf("%s %s: a pod template has a description. Run make manifests.", path, v.Name)
			}
			if n := len(regexp.MustCompile(`(?m)^\s+description: `).FindAll(before, -1)); n < 20 {
				t.Errorf("%s %s: found only %d descriptions of koodam fields", path, v.Name, n)
			}
		}
	}
}
