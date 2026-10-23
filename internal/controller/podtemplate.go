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
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
	"github.com/mohanraj00/koodam/internal/podtemplate"
)

// The names of the main containers. spec.podTemplate uses the same names.
const (
	workspaceContainer = "workspace"
	agentContainer     = "agent"
)

// reasonInvalidPodTemplate is the condition reason for a spec.podTemplate that Koodam refuses.
const reasonInvalidPodTemplate = "InvalidPodTemplate"

// invalidTemplateError is a spec.podTemplate that the merge refuses. A retry cannot fix it. The
// message has the field paths and the names only, never a value of env.
type invalidTemplateError struct{ errs field.ErrorList }

func (e *invalidTemplateError) Error() string { return e.errs.ToAggregate().Error() }

// withPodTemplate merges spec.podTemplate t into the Koodam template base. main is the name of
// the main container. If t is nil, it returns base.
func withPodTemplate(base corev1.PodTemplateSpec, t *corev1alpha1.PodTemplate, main string) (
	corev1.PodTemplateSpec, error) {
	if t == nil {
		return base, nil
	}
	u := &podtemplate.User{Labels: t.Metadata.Labels, Annotations: t.Metadata.Annotations, Spec: t.Spec}
	out, errs := podtemplate.Merge(base, u, main, field.NewPath("spec", "podTemplate"))
	if len(errs) > 0 {
		return base, &invalidTemplateError{errs: errs}
	}
	return out, nil
}
