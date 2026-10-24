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
	"errors"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"

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

// Error writes the field path, the type and the detail of each error. It does not write the
// value, so that a later check that returns a value cannot put a credential in the status.
func (e *invalidTemplateError) Error() string {
	msgs := make([]string, 0, len(e.errs))
	for _, fe := range e.errs {
		msg := fe.Field + ": " + fe.Type.String()
		if fe.Detail != "" {
			msg += ": " + fe.Detail
		}
		msgs = append(msgs, msg)
	}
	return strings.Join(msgs, "; ")
}

// isInvalidTemplate reports whether err is a refused spec.podTemplate, and not an API error that
// a retry can fix.
func isInvalidTemplate(err error) bool {
	var e *invalidTemplateError
	return errors.As(err, &e)
}

// mergedStatefulSet returns the StatefulSet of the Workspace with spec.podTemplate.
func (r *WorkspaceReconciler) mergedStatefulSet(ctx context.Context, ws *corev1alpha1.Workspace, user string) (
	*appsv1.StatefulSet, error) {
	sts := r.statefulSet(ws, user)
	tmpl, err := withPodTemplate(ctx, r.APIReader, ws.Namespace, sts.Spec.Template, ws.Spec.PodTemplate,
		workspaceContainer)
	if err != nil {
		return nil, err
	}
	r.Egress.apply(&tmpl.Spec, ws.Namespace)
	sts.Spec.Template = tmpl
	return sts, nil
}

// withPodTemplate merges spec.podTemplate t into the Koodam template base. main is the name of
// the main container. If t is nil, it returns base. It refuses a reference to a Secret in
// namespace ns that a Koodam object owns.
func withPodTemplate(ctx context.Context, rd client.Reader, ns string, base corev1.PodTemplateSpec,
	t *corev1alpha1.PodTemplate, main string) (corev1.PodTemplateSpec, error) {
	if t == nil {
		return base, nil
	}
	path := field.NewPath("spec", "podTemplate")
	u := &podtemplate.User{Labels: t.Metadata.Labels, Annotations: t.Metadata.Annotations, Spec: t.Spec}
	out, errs := podtemplate.Merge(base, u, main, path)
	if len(errs) == 0 {
		var err error
		if errs, err = ownedSecrets(ctx, rd, ns, podtemplate.SecretRefs(&t.Spec, path.Child("spec"))); err != nil {
			return base, err
		}
	}
	if len(errs) > 0 {
		return base, &invalidTemplateError{errs: errs}
	}
	return out, nil
}

// ownedSecrets refuses each reference to a Secret that a Koodam object owns, for example the
// gateway key or the host key of a different Workspace. The controller can read these Secrets,
// and the user who writes the template maybe cannot. It reads only the metadata. A Secret that
// does not exist is permitted, because the user can make it later.
func ownedSecrets(ctx context.Context, rd client.Reader, ns string, refs []podtemplate.SecretRef) (
	field.ErrorList, error) {
	var errs field.ErrorList
	owned := map[string]bool{}
	for _, ref := range refs {
		// The API server refuses the pod with an empty name. A Get with an empty name fails each time.
		if ref.Name == "" {
			continue
		}
		isOwned, seen := owned[ref.Name]
		if !seen {
			m := &metav1.PartialObjectMetadata{}
			m.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind("Secret"))
			err := rd.Get(ctx, types.NamespacedName{Namespace: ns, Name: ref.Name}, m)
			if err != nil && !apierrors.IsNotFound(err) {
				return nil, err
			}
			isOwned = err == nil && koodamOwned(m.OwnerReferences)
			owned[ref.Name] = isOwned
		}
		if isOwned {
			errs = append(errs, field.Forbidden(ref.Path, "Koodam owns this Secret."))
		}
	}
	return errs, nil
}

// koodamOwned reports whether one of refs is a Koodam object.
func koodamOwned(refs []metav1.OwnerReference) bool {
	for _, o := range refs {
		if gv, err := schema.ParseGroupVersion(o.APIVersion); err == nil && gv.Group == corev1alpha1.GroupVersion.Group {
			return true
		}
	}
	return false
}
