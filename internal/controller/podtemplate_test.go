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
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
	"github.com/mohanraj00/koodam/internal/podtemplate"
)

func TestOwnedSecrets(t *testing.T) {
	const space = "team"
	owned := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "ws-b-ssh-host", Namespace: space,
		OwnerReferences: []metav1.OwnerReference{{APIVersion: corev1alpha1.GroupVersion.String(),
			Kind: "Workspace", Name: "ws-b", UID: "ws-b-uid"}}}}
	mine := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "github-token", Namespace: space,
		OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: "d", UID: "d"}}}}
	rd := fake.NewClientBuilder().WithObjects(owned, mine).Build()
	p := field.NewPath("spec")
	refs := []podtemplate.SecretRef{
		{Name: "", Path: p.Index(0)},
		{Name: mine.Name, Path: p.Index(1)},
		{Name: "missing", Path: p.Index(2)},
		{Name: owned.Name, Path: p.Index(3)},
		{Name: owned.Name, Path: p.Index(4)},
	}
	errs, err := ownedSecrets(t.Context(), rd, space, refs)
	if err != nil {
		t.Fatal(err)
	}
	if len(errs) != 2 || errs[0].Field != "spec[3]" || errs[1].Field != "spec[4]" {
		t.Fatalf("errors = %v", errs)
	}
}
