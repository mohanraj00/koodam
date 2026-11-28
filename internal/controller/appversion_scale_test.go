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

	corev1beta1 "github.com/mohanraj00/koodam/api/v1beta1"
)

// TestScaleReplicas checks the pods that share a tenant rate and the range of a retiring version.
func TestScaleReplicas(t *testing.T) {
	av := &corev1beta1.AppVersion{Spec: corev1beta1.AppVersionSpec{Scale: &corev1beta1.AppVersionScale{
		MinReplicas: new(int32(2)), MaxReplicas: 6, Metric: corev1beta1.AppVersionScaleMetric{
			CPU: &corev1beta1.AppVersionCPUMetric{AverageUtilization: 80}}}}}
	// The tenant rate of each pod uses the maximum, so the pods never get more than the rate.
	if n := versionReplicas(av); n != 6 {
		t.Fatalf("versionReplicas = %d, want 6", n)
	}
	if h := autoscaler(av); *h.Spec.MinReplicas != 2 || h.Spec.MaxReplicas != 6 {
		t.Fatalf("active range = %d..%d, want 2..6", *h.Spec.MinReplicas, h.Spec.MaxReplicas)
	}
	av.Spec.Lifecycle = corev1beta1.AppVersionRetiring
	if h := autoscaler(av); *h.Spec.MinReplicas != 2 || h.Spec.MaxReplicas != 2 {
		t.Fatalf("retiring range = %d..%d, want 2..2", *h.Spec.MinReplicas, h.Spec.MaxReplicas)
	}
	av.Spec.Scale.MinReplicas = nil
	if n := scaleMin(av.Spec.Scale); n != 1 {
		t.Fatalf("scaleMin = %d, want 1", n)
	}
}
