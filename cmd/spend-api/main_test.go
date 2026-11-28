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
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"

	corev1beta1 "github.com/mohanraj00/koodam/api/v1beta1"
)

// The mapper has each kind that the spend API reads, as a namespaced resource.
func TestKubeMapper(t *testing.T) {
	m := kubeMapper()
	for gvk, resource := range map[schema.GroupVersionKind]string{
		corev1beta1.GroupVersion.WithKind("Agent"):       "agents",
		corev1beta1.GroupVersion.WithKind("SpendBudget"): "spendbudgets",
		{Group: "batch", Version: "v1", Kind: "Job"}:     "jobs",
		{Version: "v1", Kind: "Pod"}:                     "pods",
	} {
		kind := gvk.Kind
		mapping, err := m.RESTMapping(gvk.GroupKind(), gvk.Version)
		if err != nil {
			t.Fatalf("no mapping for %s: %v", kind, err)
		}
		if mapping.Resource.Resource != resource || mapping.Scope.Name() != meta.RESTScopeNameNamespace {
			t.Errorf("%s maps to %s, scope %s", kind, mapping.Resource.Resource, mapping.Scope.Name())
		}
	}
}
