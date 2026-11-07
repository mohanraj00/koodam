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

package ingress

import (
	"context"
	"maps"
	"slices"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
	"github.com/mohanraj00/koodam/internal/agentingress"
)

// The resolver uses only GET and LIST. It does not watch, cache, write, connect or forward.

// checkPolicies reads the configured Agent NetworkPolicy of each run namespace, in name order,
// and compares it to the guard with agentingress.CheckPolicy. It reads no other object. It stops
// at the first failure and returns ErrTargetUnavailable. A nil error proves the shape of each
// policy at the time of the read only. It does not prove that the CNI enforces the policy, or that
// no other additive policy permits more traffic. A resolver that is not configured refuses with
// ErrTargetUnavailable and makes no read, thus an empty namespace set never passes.
func (r *targetResolver) checkPolicies(ctx context.Context) error {
	if !r.configured() {
		return ErrTargetUnavailable
	}
	for _, namespace := range slices.Sorted(maps.Keys(r.guards)) {
		if err := r.checkPolicy(ctx, r.guards[namespace]); err != nil {
			return err
		}
	}
	return nil
}

// lookup reads the Agent, its run Job and the run pods, and returns the target of the sole
// eligible pod.
func (r *targetResolver) lookup(ctx context.Context, namespace, agent string) (target, error) {
	a := &corev1alpha1.Agent{}
	if err := r.get(ctx, namespace, agent, a); err != nil {
		return target{}, err
	}
	port, job, err := checkAgent(a, namespace, agent)
	if err != nil {
		return target{}, err
	}
	t := target{namespace: namespace, agent: agent, agentUID: a.UID, job: job}
	j := &batchv1.Job{}
	if err := r.get(ctx, namespace, job, j); err != nil {
		return target{}, err
	}
	if err := checkJob(j, t); err != nil {
		return target{}, err
	}
	t.jobUID = j.UID
	pods := &corev1.PodList{}
	if err := r.list(ctx, pods, client.InNamespace(namespace),
		client.MatchingLabels{targetAgentLabel: agent, targetRunLabel: job}); err != nil {
		return target{}, ErrTargetUnavailable
	}
	var selected *corev1.Pod
	for i := range pods.Items {
		if !eligiblePod(&pods.Items[i], t) {
			continue
		}
		if selected != nil {
			return target{}, ErrTargetUnavailable
		}
		selected = &pods.Items[i]
	}
	if selected == nil {
		return target{}, ErrTargetUnavailable
	}
	if t.addr, err = podAddress(selected, port); err != nil {
		return target{}, err
	}
	t.pod, t.podUID = selected.Name, selected.UID
	return t, nil
}

// checkPolicy reads the configured Agent NetworkPolicy of g and compares it to g.
func (r *targetResolver) checkPolicy(ctx context.Context, g agentingress.PolicyGuard) error {
	np := &networkingv1.NetworkPolicy{}
	if err := r.get(ctx, g.Namespace, g.Name, np); err != nil {
		return ErrTargetUnavailable
	}
	if agentingress.CheckPolicy(g, np) != nil {
		return ErrTargetUnavailable
	}
	return nil
}

// get reads the object namespace/name into obj. It returns ErrTargetAbsent if the API returns
// NotFound, and ErrTargetUnavailable for each other error. If ctx ended before the read, it does
// not read. If ctx ended during the read, it discards the result.
func (r *targetResolver) get(ctx context.Context, namespace, name string, obj client.Object) error {
	if ctx.Err() != nil {
		return ErrTargetUnavailable
	}
	err := r.reader.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, obj)
	if ctx.Err() != nil {
		return ErrTargetUnavailable
	}
	if apierrors.IsNotFound(err) {
		return ErrTargetAbsent
	}
	if err != nil {
		return ErrTargetUnavailable
	}
	return nil
}

// list reads the objects of opts into list. It checks ctx as get does. The caller gives no limit
// and no continue token. A list with a continue token or a nonzero remaining item count is partial
// and gives ErrTargetUnavailable: a partial list cannot prove that a pod is the sole eligible pod.
// list does not read a next page and does not try again.
func (r *targetResolver) list(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if ctx.Err() != nil {
		return ErrTargetUnavailable
	}
	err := r.reader.List(ctx, list, opts...)
	if err != nil || ctx.Err() != nil {
		return ErrTargetUnavailable
	}
	if list.GetContinue() != "" {
		return ErrTargetUnavailable
	}
	if n := list.GetRemainingItemCount(); n != nil && *n != 0 {
		return ErrTargetUnavailable
	}
	return nil
}

// configured is true if r has a reader and at least one run namespace. A nil or zero-value
// resolver, or one that did not come from newTargetResolver, is not configured.
func (r *targetResolver) configured() bool {
	return r != nil && r.reader != nil && len(r.guards) > 0
}
