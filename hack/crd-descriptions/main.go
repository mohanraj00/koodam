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

// Command crd-descriptions removes the description text below each Kubernetes pod template of a
// CRD. The make target manifests runs it after controller-gen and crd-optional.
//
// controller-gen copies the description of each field of corev1.PodTemplateSpec into the CRD.
// This text is most of the size of a CRD with a pod template. With two API versions in each CRD
// (#511), Helm cannot store the release of the chart, because a Secret has a limit of 1 MiB. The
// descriptions of the koodam fields stay, and the description of the pod template field stays.
// "kubectl explain pod.spec" has the text that the command removes.
//
// The command changes no validation: it removes only description values.
//
// Usage, from the repository root: go run ./hack/crd-descriptions CRD_FILE...
package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"
)

type schema = apiextensionsv1.JSONSchemaProps

// crdFile is a CustomResourceDefinition in the form that controller-gen writes. It has no status
// and no creationTimestamp, and its metadata has only the name and the annotations.
type crdFile struct {
	metav1.TypeMeta `json:",inline"`
	Metadata        struct {
		Annotations map[string]string `json:"annotations"`
		Name        string            `json:"name"`
	} `json:"metadata"`
	Spec apiextensionsv1.CustomResourceDefinitionSpec `json:"spec"`
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: crd-descriptions CRD_FILE...")
		os.Exit(2)
	}
	for _, path := range os.Args[1:] {
		if err := run(path); err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", path, err)
			os.Exit(1)
		}
	}
}

func run(path string) error {
	in, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	out, err := patch(in)
	if err != nil {
		return err
	}
	return os.WriteFile(path, out, 0o644) //nolint:gosec // The CRD is a public file.
}

// patch returns the CRD with no description below its pod templates. The input is the
// controller-gen output, or the output of an earlier run. Thus a second run makes no change.
func patch(in []byte) ([]byte, error) {
	crd := &crdFile{}
	if err := yaml.UnmarshalStrict(in, crd); err != nil {
		return nil, errors.New("the input is not a valid YAML CustomResourceDefinition")
	}
	// The schema types decode some fields with no strict check. Thus a new encode must give the
	// input again. This refuses an unknown or lost field at each level and a change of format.
	if out, err := marshal(crd); err != nil || !bytes.Equal(out, in) {
		return nil, errors.New("want one YAML document in the controller-gen format with no unknown field")
	}
	for i := range crd.Spec.Versions {
		v := &crd.Spec.Versions[i]
		if v.Schema == nil || v.Schema.OpenAPIV3Schema == nil || clean(v.Schema.OpenAPIV3Schema) == 0 {
			return nil, fmt.Errorf("the version %s has no pod template", v.Name)
		}
	}
	return marshal(crd)
}

func marshal(crd *crdFile) ([]byte, error) {
	b, err := yaml.Marshal(crd)
	if err != nil {
		return nil, err
	}
	return append([]byte("---\n"), b...), nil
}

// clean removes each description below each pod template in s. It returns the number of pod
// templates.
func clean(s *schema) int {
	if isPodTemplate(s) {
		children(s, strip)
		return 1
	}
	n := 0
	children(s, func(c *schema) { n += clean(c) })
	return n
}

// isPodTemplate is true for the schema of corev1.PodTemplateSpec: an object with a spec that has
// containers.
func isPodTemplate(s *schema) bool {
	spec, ok := s.Properties["spec"]
	if !ok {
		return false
	}
	_, ok = spec.Properties["containers"]
	return ok
}

func strip(s *schema) {
	s.Description = ""
	children(s, strip)
}

// children calls f for each schema that s contains directly.
func children(s *schema, f func(*schema)) {
	if s.Items != nil {
		if s.Items.Schema != nil {
			f(s.Items.Schema)
		}
		list(s.Items.JSONSchemas, f)
	}
	list(s.AllOf, f)
	list(s.OneOf, f)
	list(s.AnyOf, f)
	if s.Not != nil {
		f(s.Not)
	}
	for _, m := range []map[string]schema{s.Properties, s.PatternProperties, s.Definitions} {
		for name, c := range m {
			f(&c)
			m[name] = c
		}
	}
	if s.AdditionalProperties != nil && s.AdditionalProperties.Schema != nil {
		f(s.AdditionalProperties.Schema)
	}
	if s.AdditionalItems != nil && s.AdditionalItems.Schema != nil {
		f(s.AdditionalItems.Schema)
	}
	for _, d := range s.Dependencies {
		if d.Schema != nil {
			f(d.Schema)
		}
	}
}

func list(schemas []schema, f func(*schema)) {
	for i := range schemas {
		f(&schemas[i])
	}
}
