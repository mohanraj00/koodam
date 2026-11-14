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
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
)

const (
	// claimContainer is the name of the container that checks the signed claim of a caller.
	claimContainer = "koodam-claim"
	// claimDir has the JWKS file in the check container.
	claimDir = "/etc/koodam-claim"
	// claimJWKSAnnotation has the JWKS of the issuer in the pod. The keys are public. A change
	// of the JWKS changes the pod template, so the Deployment makes new pods.
	claimJWKSAnnotation = "koodam.dev/claim-jwks"
	// claimPortBase is the first port that the check container can use.
	claimPortBase = 15080
	// condClaimCheck shows if the claim check protects the version.
	condClaimCheck = "ClaimCheck"
)

// AppClaimCheck is the configuration of the signed claim check at the ingress of each App. The
// operator configures it. If Issuer is empty, the pods have no check container.
type AppClaimCheck struct {
	Issuer, Audience, CallerClaim, TenantClaim string
	// JWKS is the JWKS document of the issuer.
	JWKS string
	// Image is the image with /app-claim-check.
	Image string
}

func (c AppClaimCheck) enabled() bool { return c.Issuer != "" }

// listenPorts gives back one port of the check container for each port of the app container,
// in the same order. If the check is off, it gives back the ports of the app container. A
// listen port is never a port of the app container.
func (c AppClaimCheck) listenPorts(app []corev1.ContainerPort) []corev1.ContainerPort {
	if !c.enabled() {
		return app
	}
	used := map[int32]bool{}
	for _, p := range app {
		used[p.ContainerPort] = true
	}
	out := make([]corev1.ContainerPort, 0, len(app))
	next := int32(claimPortBase)
	for range app {
		for used[next] {
			next++
		}
		out = append(out, corev1.ContainerPort{ContainerPort: next, Protocol: corev1.ProtocolTCP})
		next++
	}
	return out
}

// add puts the check container in the pod template. The container is a sidecar, so it starts
// before the app container. The Service and the NetworkPolicy of the version use its ports.
func (c AppClaimCheck) add(tmpl *corev1.PodTemplateSpec, av *corev1alpha1.AppVersion, app []corev1.ContainerPort) {
	if !c.enabled() {
		return
	}
	listen := c.listenPorts(app)
	args := []string{"--issuer", c.Issuer, "--audience", c.Audience, "--caller-claim", c.CallerClaim,
		"--tenant-claim", c.TenantClaim, "--jwks-file", claimDir + "/jwks.json",
		"--app", av.Spec.AppRef.Name, "--version", av.Name}
	for i, p := range app {
		args = append(args, "--route", fmt.Sprintf("%d=%d", listen[i].ContainerPort, p.ContainerPort))
	}
	metav1.SetMetaDataAnnotation(&tmpl.ObjectMeta, claimJWKSAnnotation, c.JWKS)
	tmpl.Spec.Volumes = append(tmpl.Spec.Volumes, corev1.Volume{Name: claimContainer, VolumeSource: corev1.VolumeSource{
		DownwardAPI: &corev1.DownwardAPIVolumeSource{Items: []corev1.DownwardAPIVolumeFile{{Path: "jwks.json",
			FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.annotations['" + claimJWKSAnnotation + "']"}}}}}})
	tmpl.Spec.InitContainers = append(tmpl.Spec.InitContainers, corev1.Container{
		Name: claimContainer, Image: c.Image, Command: []string{"/app-claim-check"}, Args: args, Ports: listen,
		RestartPolicy:   new(corev1.ContainerRestartPolicyAlways),
		VolumeMounts:    []corev1.VolumeMount{{Name: claimContainer, MountPath: claimDir, ReadOnly: true}},
		SecurityContext: helperSecurityContext(),
		Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
			corev1.ResourceCPU: resource.MustParse("10m"), corev1.ResourceMemory: resource.MustParse("16Mi")}},
	})
}

// condition gives back the condition ClaimCheck. The check protects the version only if the
// NetworkPolicy of the version exists: with no policy, a pod of the cluster can call the app
// container directly. policy is the status of the condition TracedEgress.
func (c AppClaimCheck) condition(policy metav1.ConditionStatus, generation int64) metav1.Condition {
	cond := metav1.Condition{Type: condClaimCheck, Status: metav1.ConditionTrue, Reason: "Enforced",
		Message:            "Each request to the version needs a signed claim of the issuer.",
		ObservedGeneration: generation}
	if policy != metav1.ConditionTrue {
		cond.Status, cond.Reason = metav1.ConditionFalse, "NoNetworkPolicy"
		cond.Message = "The version has no NetworkPolicy, so a pod of the cluster can call the app container with no claim."
	}
	return cond
}
