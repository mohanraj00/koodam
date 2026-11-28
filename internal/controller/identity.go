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
	"context"
	"fmt"
	"maps"
	"slices"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1beta1 "github.com/mohanraj00/koodam/api/v1beta1"
)

// The egress proxy reads these pod labels to find the run and the user of a call.
const (
	// RunLabel is on the pod of an Agent run. The value is the name of the run Job.
	RunLabel = "koodam.dev/run"
	// UserLabel is on the Agent and Workspace pods. The value is the name of the Profile that
	// binds the namespace. An admin controls the binding, thus the pod spec cannot change the value.
	UserLabel = "koodam.dev/user"
)

// boundProfile reads the namespace and the Profile in its label, and applies namespaceBound.
// It returns the Profile name, or "" if no Profile binds the namespace. It returns no error if
// the namespace, the label or the Profile does not exist. The Profile name is also the value of
// a namespace label, thus it is a valid label value.
func boundProfile(ctx context.Context, c client.Reader, namespace string) (string, error) {
	p, err := boundProfileObject(ctx, c, namespace)
	if p == nil || err != nil {
		return "", err
	}
	return p.Name, nil
}

// boundProfileObject returns the bound Profile, or nil if no Profile binds the namespace.
func boundProfileObject(ctx context.Context, c client.Reader, namespace string) (*corev1beta1.Profile, error) {
	ns := &corev1.Namespace{}
	if err := c.Get(ctx, types.NamespacedName{Name: namespace}, ns); err != nil {
		return nil, client.IgnoreNotFound(err)
	}
	name := ns.Labels[corev1beta1.ProfileLabel]
	if name == "" {
		return nil, nil
	}
	p := &corev1beta1.Profile{}
	if err := c.Get(ctx, types.NamespacedName{Name: name}, p); err != nil {
		return nil, client.IgnoreNotFound(err)
	}
	if !namespaceBound(p, ns) {
		return nil, nil
	}
	return p, nil
}

const reasonCostGroupNotAllowed = "CostGroupNotAllowed"

// costGroupMessage checks the cost group against the Profile of the namespace.
// An empty message permits the value. A read error must be retried.
func costGroupMessage(ctx context.Context, c client.Reader, namespace, value string) (string, error) {
	if value == "" {
		return "", nil
	}
	p, err := boundProfileObject(ctx, c, namespace)
	if err != nil {
		return "", err
	}
	if p == nil {
		return fmt.Sprintf("the label koodam.dev/cost-group needs a Profile bound to namespace %s, with the value in spec.costGroups", namespace), nil
	}
	if !slices.Contains(p.Spec.CostGroups, value) {
		return fmt.Sprintf("the label koodam.dev/cost-group has the value %q, and Profile %s does not permit it in spec.costGroups", value, p.Name), nil
	}
	return "", nil
}

// addIdentity adds the run and the user labels to the pod metadata m. It does not add an empty
// value or a value that is not a valid label value. It copies the label map first, because a
// selector can share the map.
func addIdentity(m *metav1.ObjectMeta, run, user string) {
	m.Labels = maps.Clone(m.Labels)
	addLabel(m, RunLabel, run)
	addLabel(m, UserLabel, user)
}

func addLabel(m *metav1.ObjectMeta, key, value string) {
	if value == "" || len(validation.IsValidLabelValue(value)) > 0 {
		return
	}
	if m.Labels == nil {
		m.Labels = map[string]string{}
	}
	m.Labels[key] = value
}
