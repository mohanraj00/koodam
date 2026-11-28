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

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	corev1beta1 "github.com/mohanraj00/koodam/api/v1beta1"
)

// scaleMin is the smallest number of pods of a version with a scale.
func scaleMin(s *corev1beta1.AppVersionScale) int32 {
	if s.MinReplicas != nil {
		return *s.MinReplicas
	}
	return 1
}

// autoscaler gives back the HorizontalPodAutoscaler of a version with a scale. A version in its
// drain stays at its minimum, so the load of old sessions cannot keep a retired version large.
func autoscaler(av *corev1beta1.AppVersion) *autoscalingv2.HorizontalPodAutoscaler {
	s := av.Spec.Scale
	low, high := scaleMin(s), s.MaxReplicas
	if av.Spec.Lifecycle == corev1beta1.AppVersionRetiring {
		high = low
	}
	var m autoscalingv2.MetricSpec
	if s.Metric.CPU != nil {
		m = autoscalingv2.MetricSpec{Type: autoscalingv2.ResourceMetricSourceType,
			Resource: &autoscalingv2.ResourceMetricSource{Name: corev1.ResourceCPU,
				Target: autoscalingv2.MetricTarget{Type: autoscalingv2.UtilizationMetricType,
					AverageUtilization: new(s.Metric.CPU.AverageUtilization)}}}
	} else {
		m = autoscalingv2.MetricSpec{Type: autoscalingv2.PodsMetricSourceType,
			Pods: &autoscalingv2.PodsMetricSource{Metric: autoscalingv2.MetricIdentifier{Name: s.Metric.Pods.Name},
				Target: autoscalingv2.MetricTarget{Type: autoscalingv2.AverageValueMetricType,
					AverageValue: new(s.Metric.Pods.AverageValue.DeepCopy())}}}
	}
	return &autoscalingv2.HorizontalPodAutoscaler{ObjectMeta: appVersionMeta(av),
		Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
			ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{APIVersion: "apps/v1", Kind: "Deployment",
				Name: appVersionName(av)},
			MinReplicas: &low, MaxReplicas: high, Metrics: []autoscalingv2.MetricSpec{m}}}
}

// applyScale makes or updates the HorizontalPodAutoscaler of a version with a scale. It deletes
// the autoscaler of a version with no scale. It returns a reason if an autoscaler with the version
// name is not from av, or if the API server refuses the autoscaler.
func (r *AppVersionReconciler) applyScale(ctx context.Context, av *corev1beta1.AppVersion) (string, error) {
	if av.Spec.Scale == nil {
		return "", r.deleteOwned(ctx, av, &autoscalingv2.HorizontalPodAutoscaler{})
	}
	want := autoscaler(av)
	have := &autoscalingv2.HorizontalPodAutoscaler{}
	found, err := r.getOwned(ctx, av, have)
	switch {
	case errors.Is(err, errNotOwned):
		return reasonNameConflict, nil
	case err != nil:
		return "", err
	case !found:
		err = r.Create(ctx, want)
	case !sameScale(have.Spec, want.Spec):
		// The API server sets the default behavior. The update keeps it.
		have.Spec.ScaleTargetRef, have.Spec.MinReplicas = want.Spec.ScaleTargetRef, want.Spec.MinReplicas
		have.Spec.MaxReplicas, have.Spec.Metrics = want.Spec.MaxReplicas, want.Spec.Metrics
		err = r.Update(ctx, have)
	}
	if permanent(err) {
		logf.FromContext(ctx).Info("The API server refused the HorizontalPodAutoscaler of the version")
		return reasonRefused, nil
	}
	return "", err
}

// sameScale tells if have has the fields of want that the controller sets.
func sameScale(have, want autoscalingv2.HorizontalPodAutoscalerSpec) bool {
	return have.ScaleTargetRef == want.ScaleTargetRef && have.MaxReplicas == want.MaxReplicas &&
		equality.Semantic.DeepEqual(have.MinReplicas, want.MinReplicas) &&
		equality.Semantic.DeepEqual(have.Metrics, want.Metrics)
}
