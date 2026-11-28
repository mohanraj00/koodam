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
	"slices"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/validation/field"

	corev1beta1 "github.com/mohanraj00/koodam/api/v1beta1"
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
	// msgClaimName is the message for a template that uses the name of the check container.
	msgClaimName = "the name is reserved for the claim check container"
	// reasonClaimNotApplied is the reason of ClaimCheck=False while the objects of the version
	// stay from an earlier template.
	reasonClaimNotApplied = "NotApplied"
	// condClaimCheck shows if the claim check protects the version.
	condClaimCheck = "ClaimCheck"
)

// AppClaimCheck is the configuration of the signed claim check at the ingress of each App. The
// operator configures it. If Issuer is empty, the pods have no check container.
type AppClaimCheck struct {
	Issuer, Audience, CallerClaim, TenantClaim string
	// Zone is the zone of the install. ZoneClaim is the claim with the zone of the caller. With
	// a zone claim, the check refuses a caller of a different zone.
	Zone, ZoneClaim string
	// RequireZoneClaim refuses a token with no zone claim.
	RequireZoneClaim bool
	// JWKS is the JWKS document of the issuer.
	JWKS string
	// Image is the image with /app-claim-check.
	Image string
	// TenantToken makes the check container sign a tenant token for the tenant of each request.
	// It has an effect only with a tenant claim.
	TenantToken bool
	// Tenants is the configuration for a tenant label with no TenantQuota.
	Tenants AppTenantLimits
}

func (c AppClaimCheck) enabled() bool { return c.Issuer != "" }

// listenPorts gives back one port of the check container for each port of the app container,
// in the same order. If the check is off, it gives back the ports of the app container. A
// listen port is never a port that a container of the pod declares, because all the containers
// of a pod share one network namespace.
func (c AppClaimCheck) listenPorts(app []corev1.ContainerPort, pod *corev1.PodSpec) []corev1.ContainerPort {
	if !c.enabled() {
		return app
	}
	used := map[int32]bool{}
	for _, p := range app {
		used[p.ContainerPort] = true
	}
	if pod != nil {
		for _, ct := range slices.Concat(pod.Containers, pod.InitContainers) {
			for _, p := range ct.Ports {
				used[p.ContainerPort] = true
			}
		}
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

// zoneArgs gives back the zone flags of the check container. An install with no zone has none,
// so its pod template does not change.
func (c AppClaimCheck) zoneArgs() []string {
	if c.Zone == "" {
		return nil
	}
	args := []string{"--zone", c.Zone}
	if c.ZoneClaim != "" {
		args = append(args, "--zone-claim", c.ZoneClaim)
	}
	if c.RequireZoneClaim {
		args = append(args, "--zone-require-claim")
	}
	return args
}

// claimPorts gives back the ports of the check container of the pod. If the check is off or the
// pod has no check container, it gives back the ports of the app container. With the check
// off, a container of the user can have the name of the check container, and its ports must
// not control the NetworkPolicy.
func (c AppClaimCheck) claimPorts(pod *corev1.PodSpec, app []corev1.ContainerPort) []corev1.ContainerPort {
	if !c.enabled() {
		return app
	}
	for i := range pod.InitContainers {
		if pod.InitContainers[i].Name == claimContainer {
			return pod.InitContainers[i].Ports
		}
	}
	return app
}

// add puts the check container in the pod template. The container is the first init container
// and a sidecar with a startup probe on its last port. Thus it binds all its ports before a
// container of the user starts. The Service and the NetworkPolicy of the version use its ports. It
// refuses a template that has a container or a volume with the name of the check container.
func (c AppClaimCheck) add(tmpl *corev1.PodTemplateSpec, av *corev1beta1.AppVersion, app []corev1.ContainerPort) error {
	if !c.enabled() {
		return nil
	}
	path := field.NewPath("spec", "podTemplate", "spec")
	var errs field.ErrorList
	for _, ct := range slices.Concat(tmpl.Spec.Containers, tmpl.Spec.InitContainers) {
		if ct.Name == claimContainer {
			errs = append(errs, field.Invalid(path.Child("containers"), ct.Name, msgClaimName))
		}
	}
	for _, v := range tmpl.Spec.Volumes {
		if v.Name == claimContainer || (c.tenantsEnabled() && v.Name == tenantsVolume) {
			errs = append(errs, field.Invalid(path.Child("volumes"), v.Name, msgClaimName))
		}
	}
	if len(errs) > 0 {
		return &invalidTemplateError{errs: errs}
	}
	listen := c.listenPorts(app, &tmpl.Spec)
	args := []string{"--issuer", c.Issuer, "--audience", c.Audience, "--caller-claim", c.CallerClaim,
		"--tenant-claim", c.TenantClaim, "--jwks-file", claimDir + "/jwks.json",
		"--app", av.Spec.AppRef.Name, "--version", av.Name}
	args = append(args, c.zoneArgs()...)
	for i, p := range app {
		args = append(args, "--route", fmt.Sprintf("%d=%d", listen[i].ContainerPort, p.ContainerPort))
	}
	metav1.SetMetaDataAnnotation(&tmpl.ObjectMeta, claimJWKSAnnotation, c.JWKS)
	tmpl.Spec.Volumes = append(tmpl.Spec.Volumes, corev1.Volume{Name: claimContainer, VolumeSource: corev1.VolumeSource{
		DownwardAPI: &corev1.DownwardAPIVolumeSource{Items: []corev1.DownwardAPIVolumeFile{{Path: "jwks.json",
			FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.annotations['" + claimJWKSAnnotation + "']"}}}}}})
	mounts := []corev1.VolumeMount{{Name: claimContainer, MountPath: claimDir, ReadOnly: true}}
	if c.tenantsEnabled() {
		// The volume is the full ConfigMap with no subPath, so the kubelet updates the file.
		args = append(args, "--tenants-file", tenantsDir+"/"+tenantsFile)
		tmpl.Spec.Volumes = append(tmpl.Spec.Volumes, corev1.Volume{Name: tenantsVolume, VolumeSource: corev1.VolumeSource{
			ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: tenantLimitsName(av)}}}})
		mounts = append(mounts, corev1.VolumeMount{Name: tenantsVolume, MountPath: tenantsDir, ReadOnly: true})
	}
	check := corev1.Container{
		Name: claimContainer, Image: c.Image, Command: []string{"/app-claim-check"}, Args: args, Ports: listen,
		RestartPolicy:   new(corev1.ContainerRestartPolicyAlways),
		VolumeMounts:    mounts,
		SecurityContext: helperSecurityContext(),
		Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
			corev1.ResourceCPU: resource.MustParse("10m"), corev1.ResourceMemory: resource.MustParse("16Mi")}},
	}
	if n := len(listen); n > 0 {
		// The check binds its ports in order, so the last port shows that it has all of them.
		check.StartupProbe = &corev1.Probe{PeriodSeconds: 1, FailureThreshold: 60, ProbeHandler: corev1.ProbeHandler{
			TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt32(listen[n-1].ContainerPort)}}}
	}
	tmpl.Spec.InitContainers = append([]corev1.Container{check}, tmpl.Spec.InitContainers...)
	return nil
}

// condition gives back the condition ClaimCheck. The check protects the version only if the
// NetworkPolicy of the version exists: with no policy, a pod of the cluster can call the app
// container directly. policy is the status of the condition TracedEgress. applied shows that
// this reconcile made the Deployment, the Service and the NetworkPolicy equal to the template
// with the check. Objects that stay from an earlier template can send traffic to the app
// container with no check, so the condition is not true for them.
func (c AppClaimCheck) condition(policy metav1.ConditionStatus, applied bool, generation int64) metav1.Condition {
	cond := metav1.Condition{Type: condClaimCheck, Status: metav1.ConditionTrue, Reason: "Enforced",
		Message:            "Each request to the version needs a signed claim of the issuer.",
		ObservedGeneration: generation}
	switch {
	case policy != metav1.ConditionTrue:
		cond.Status, cond.Reason = metav1.ConditionFalse, "NoNetworkPolicy"
		cond.Message = "The version has no NetworkPolicy, so a pod of the cluster can call the app container with no claim."
	case !applied:
		cond.Status, cond.Reason = metav1.ConditionFalse, reasonClaimNotApplied
		cond.Message = "The controller did not apply the objects of the version, so the objects can be without the claim check."
	}
	return cond
}
