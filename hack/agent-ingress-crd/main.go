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

// Command agent-ingress-crd makes the two variants of the Agent CRD for #147. No make target runs
// it yet. A later #147 change adds spec.ingressPort to the API and runs this command in the make
// target manifests, after controller-gen and crd-optional. The default variant stays in its
// place. It gets the annotation koodam.dev/agent-ingress-mode=disabled and one more AgentSpec
// rule that refuses spec.ingressPort. The enabled variant gets the annotation enabled and does
// not have that rule. All other parts of the two variants are the same.
//
// The enabled variant is only an input for a future protected installer. It is not an install.
//
// The command uses the same YAML encoder as controller-gen, so the output has the same format.
package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"
)

const (
	modeKey     = "koodam.dev/agent-ingress-mode"
	genKey      = "controller-gen.kubebuilder.io/version"
	disabled    = "disabled"
	enabled     = "enabled"
	gateRule    = "!has(self.ingressPort)"
	gateMessage = "spec.ingressPort is disabled. The traced ingress feature is not installed in this cluster."
	// immutableRule must be the same as the AgentSpec marker that the API change adds to
	// api/v1alpha1/agent_types.go.
	immutableRule = "has(self.ingressPort) == has(oldSelf.ingressPort) && " +
		"(!has(self.ingressPort) || self.ingressPort == oldSelf.ingressPort)"
	docStart  = "---\n"
	specField = "spec"
	portField = "ingressPort"
)

var gate = apiextensionsv1.ValidationRule{Rule: gateRule, Message: gateMessage}

// agentCRD is a CustomResourceDefinition in the form that controller-gen writes. It has no status
// and no creationTimestamp, and its metadata has only the name and the annotations.
type agentCRD struct {
	metav1.TypeMeta `json:",inline"`
	Metadata        struct {
		Annotations map[string]string `json:"annotations"`
		Name        string            `json:"name"`
	} `json:"metadata"`
	Spec apiextensionsv1.CustomResourceDefinitionSpec `json:"spec"`
}

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: agent-ingress-crd DEFAULT_CRD ENABLED_CRD")
		os.Exit(2)
	}
	if err := run(os.Args[1], os.Args[2]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(defaultPath, enabledPath string) error {
	in, err := os.ReadFile(defaultPath)
	if err != nil {
		return err
	}
	def, en, err := variants(in)
	if err != nil {
		return fmt.Errorf("%s: %w", defaultPath, err)
	}
	if sameFile(defaultPath, enabledPath) {
		return errors.New("the default and the enabled CRD must be two different files")
	}
	if err := os.MkdirAll(filepath.Dir(enabledPath), 0o755); err != nil {
		return err
	}
	// The default variant is replaced first. If the process stops after it, the default CRD refuses
	// spec.ingressPort, and a new run on it makes both variants again.
	return writeAll(&file{path: defaultPath, data: def}, &file{path: enabledPath, data: en})
}

// variants returns the default and the enabled CRD. The input is the controller-gen output, or the
// default variant from an earlier run. Thus a second run makes no change.
func variants(in []byte) (def, en []byte, err error) {
	crd, err := parse(in)
	if err != nil {
		return nil, nil, err
	}
	if err := base(crd); err != nil {
		return nil, nil, err
	}
	crd.Metadata.Annotations[modeKey] = enabled
	if en, err = marshal(crd); err != nil {
		return nil, nil, err
	}
	crd.Metadata.Annotations[modeKey] = disabled
	spec := props(crd)[specField]
	spec.XValidations = append(spec.XValidations, gate)
	props(crd)[specField] = spec
	if def, err = marshal(crd); err != nil {
		return nil, nil, err
	}
	return def, en, nil
}

// parse reads one CRD. The strict decode refuses duplicate keys and unknown fields. But the schema
// types decode items, additionalProperties and dependencies with no strict check. Thus parse also
// requires that a new encode gives the input again. This refuses an unknown or lost field at each
// level, a second document and a change of format.
func parse(in []byte) (*agentCRD, error) {
	crd := &agentCRD{}
	if err := yaml.UnmarshalStrict(in, crd); err != nil {
		return nil, errors.New("the input is not a valid YAML CustomResourceDefinition")
	}
	if out, err := marshal(crd); err != nil || !bytes.Equal(out, in) {
		return nil, errors.New("want one YAML document in the controller-gen format with no unknown field")
	}
	return crd, nil
}

func marshal(crd *agentCRD) ([]byte, error) {
	b, err := yaml.Marshal(crd)
	if err != nil {
		return nil, err
	}
	return append([]byte(docStart), b...), nil
}

// props returns the properties of the root schema. checkAgent makes sure that the root schema exists.
func props(crd *agentCRD) map[string]apiextensionsv1.JSONSchemaProps {
	return crd.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties
}

// base checks that crd is the Agent CRD, and removes the mode annotation and the disabled gate.
// The errors have fixed text.
func base(crd *agentCRD) error {
	ann := crd.Metadata.Annotations
	mode, hasMode := ann[modeKey]
	delete(ann, modeKey)
	if err := checkAgent(crd); err != nil {
		return err
	}
	spec := props(crd)[specField]
	if err := checkPort(spec); err != nil {
		return err
	}
	gates, err := countGates(spec.XValidations)
	if err != nil {
		return err
	}
	fresh := gates == 0 && !hasMode
	earlier := gates == 1 && mode == disabled
	if !fresh && !earlier {
		return errors.New("want the controller-gen output or the default variant")
	}
	spec.XValidations = spec.XValidations[:len(spec.XValidations)-gates]
	props(crd)[specField] = spec
	return nil
}

// checkAgent checks the CRD header. The caller removes the mode annotation before this check.
func checkAgent(crd *agentCRD) error {
	if crd.APIVersion != "apiextensions.k8s.io/v1" || crd.Kind != "CustomResourceDefinition" ||
		crd.Metadata.Name != "agents.core.koodam.dev" {
		return errors.New("want the CustomResourceDefinition agents.core.koodam.dev")
	}
	if _, ok := crd.Metadata.Annotations[genKey]; !ok || len(crd.Metadata.Annotations) != 1 {
		return errors.New("want only the controller-gen annotation and the mode annotation")
	}
	s := crd.Spec
	if s.Group != "core.koodam.dev" || s.Scope != apiextensionsv1.NamespaceScoped || s.Names.Kind != "Agent" ||
		s.Names.Plural != "agents" || len(s.Versions) != 1 || s.Versions[0].Name != "v1alpha1" ||
		!s.Versions[0].Served || !s.Versions[0].Storage || s.Versions[0].Schema == nil ||
		s.Versions[0].Schema.OpenAPIV3Schema == nil {
		return errors.New("want only the served and stored v1alpha1 version of the namespaced Agent")
	}
	return nil
}

// checkPort checks that spec.ingressPort is optional, has the bounds and has no default.
func checkPort(spec apiextensionsv1.JSONSchemaProps) error {
	port := spec.Properties[portField]
	want := apiextensionsv1.JSONSchemaProps{Description: port.Description, Type: "integer", Format: "int32",
		Minimum: new(float64(1)), Maximum: new(float64(65535))}
	if port.Description == "" || !reflect.DeepEqual(port, want) || slices.Contains(spec.Required, portField) {
		return errors.New("want an optional int32 spec.ingressPort from 1 to 65535 with no default")
	}
	return nil
}

// countGates returns 0 or 1. Only one gate is permitted, as the last rule, with no other field.
// The immutability rule must occur one time.
func countGates(rules []apiextensionsv1.ValidationRule) (int, error) {
	gates, immutable := 0, 0
	for i, r := range rules {
		if r.Rule == gateRule || r.Message == gateMessage {
			if r != gate || i != len(rules)-1 {
				return 0, errors.New("found a changed, duplicate or moved disabled gate")
			}
			gates++
		}
		if r.Rule == immutableRule {
			immutable++
		}
	}
	if immutable != 1 {
		return 0, errors.New("want one spec.ingressPort immutability rule")
	}
	return gates, nil
}
