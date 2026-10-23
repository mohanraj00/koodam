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

// Package podtemplate merges the pod template of a user with the pod template that Koodam makes.
// The Agent and the Workspace use the same merge. Koodam owns some fields. A user value for an
// owned field is an error, so Koodam never replaces a user value with no message.
package podtemplate

import (
	"fmt"
	"maps"
	pathpkg "path"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

// ReservedPrefix is the prefix of the label and annotation keys that Koodam owns.
const ReservedPrefix = "koodam.dev/"

// User is the part of the pod template that a user writes.
type User struct {
	Labels      map[string]string
	Annotations map[string]string
	Spec        corev1.PodSpec
}

// Merge returns the base template of Koodam with the user template. main is the name of the
// main container in base. path is the field path of the user template, for the errors.
//
// The user can set each pod field that Koodam does not own, sidecars, init containers and
// volumes. In the main container, the user can set only image, command, args, workingDir,
// env, envFrom, resources and more volumeMounts. A user name that is the same as a name in
// base is an error. Koodam keeps its labels, its init containers first and the main
// container first.
func Merge(base corev1.PodTemplateSpec, user *User, main string, path *field.Path) (
	corev1.PodTemplateSpec, field.ErrorList) {
	if user == nil {
		return *base.DeepCopy(), nil
	}
	errs := Validate(base, user, main, path)
	if len(errs) > 0 {
		return corev1.PodTemplateSpec{}, errs
	}
	b, u := base.Spec.DeepCopy(), user.Spec.DeepCopy()
	out := corev1.PodTemplateSpec{ObjectMeta: *base.ObjectMeta.DeepCopy(), Spec: *u}
	out.Labels = merged(user.Labels, base.Labels)
	out.Annotations = merged(user.Annotations, base.Annotations)

	s := &out.Spec
	s.AutomountServiceAccountToken = b.AutomountServiceAccountToken
	s.EnableServiceLinks = b.EnableServiceLinks
	s.SecurityContext = b.SecurityContext
	s.ServiceAccountName = b.ServiceAccountName
	s.RestartPolicy = b.RestartPolicy
	s.ActiveDeadlineSeconds = b.ActiveDeadlineSeconds
	s.InitContainers = append(append([]corev1.Container{}, b.InitContainers...), u.InitContainers...)
	s.Volumes = append(append([]corev1.Volume{}, b.Volumes...), u.Volumes...)

	containers := make([]corev1.Container, 0, len(b.Containers)+len(u.Containers))
	for _, c := range b.Containers {
		if c.Name == main {
			c = mergeMain(c, find(u.Containers, main))
		}
		containers = append(containers, c)
	}
	for _, c := range u.Containers {
		if c.Name != main {
			containers = append(containers, c)
		}
	}
	s.Containers = containers
	return out, nil
}

// mergeMain adds the fields of the user main container u to the Koodam main container c. A user
// resource replaces the Koodam value of the same resource only. The other Koodam values stay.
func mergeMain(c corev1.Container, u *corev1.Container) corev1.Container {
	if u == nil {
		return c
	}
	if u.Image != "" {
		c.Image = u.Image
	}
	if len(u.Command) > 0 {
		c.Command = u.Command
	}
	if len(u.Args) > 0 {
		c.Args = u.Args
	}
	if u.WorkingDir != "" {
		c.WorkingDir = u.WorkingDir
	}
	c.Env = append(c.Env, u.Env...)
	c.EnvFrom = append(c.EnvFrom, u.EnvFrom...)
	c.VolumeMounts = append(c.VolumeMounts, u.VolumeMounts...)
	c.Resources.Limits = mergedResources(c.Resources.Limits, u.Resources.Limits)
	c.Resources.Requests = mergedResources(c.Resources.Requests, u.Resources.Requests)
	c.Resources.Claims = append(c.Resources.Claims, u.Resources.Claims...)
	return c
}

// Validate returns an error for each user value that Koodam owns or that has the same name as
// a Koodam object in base.
func Validate(base corev1.PodTemplateSpec, user *User, main string, path *field.Path) field.ErrorList {
	if user == nil {
		return nil
	}
	var errs field.ErrorList
	meta := path.Child("metadata")
	errs = append(errs, reservedKeys(user.Labels, meta.Child("labels"))...)
	errs = append(errs, reservedKeys(user.Annotations, meta.Child("annotations"))...)

	spec, u := path.Child("spec"), &user.Spec
	owned := map[string]bool{
		"automountServiceAccountToken": u.AutomountServiceAccountToken != nil,
		"enableServiceLinks":           u.EnableServiceLinks != nil,
		"securityContext":              u.SecurityContext != nil,
		"serviceAccountName":           u.ServiceAccountName != "" || u.DeprecatedServiceAccount != "",
		"restartPolicy":                u.RestartPolicy != "",
		"activeDeadlineSeconds":        u.ActiveDeadlineSeconds != nil,
		"hostNetwork":                  u.HostNetwork,
		"hostPID":                      u.HostPID,
		"hostIPC":                      u.HostIPC,
		"hostUsers":                    u.HostUsers != nil && *u.HostUsers,
		"shareProcessNamespace":        u.ShareProcessNamespace != nil && *u.ShareProcessNamespace,
		"ephemeralContainers":          len(u.EphemeralContainers) > 0,
	}
	for _, name := range ownedOrder {
		if owned[name] {
			errs = append(errs, field.Forbidden(spec.Child(name), "Koodam owns this field."))
		}
	}

	b := base.Spec
	taken := names(b.InitContainers, b.Containers)
	vols := map[string]bool{}
	for _, v := range b.Volumes {
		vols[v.Name] = true
	}
	seen := map[string]bool{}
	for i, c := range u.InitContainers {
		p := spec.Child("initContainers").Index(i)
		if taken[c.Name] || seen[c.Name] {
			errs = append(errs, field.Duplicate(p.Child("name"), c.Name))
		}
		seen[c.Name] = true
		errs = append(errs, koodamVolumes(c, vols, p)...)
		errs = append(errs, hostAccess(c, p)...)
	}
	for i, c := range u.Containers {
		p := spec.Child("containers").Index(i)
		switch {
		case seen[c.Name]:
			errs = append(errs, field.Duplicate(p.Child("name"), c.Name))
		case c.Name == main:
			errs = append(errs, validateMain(find(b.Containers, main), c, p)...)
		case taken[c.Name]:
			errs = append(errs, field.Duplicate(p.Child("name"), c.Name))
		}
		seen[c.Name] = true
		errs = append(errs, koodamVolumes(c, vols, p)...)
		errs = append(errs, hostAccess(c, p)...)
	}
	for i, v := range u.Volumes {
		p := spec.Child("volumes").Index(i)
		if vols[v.Name] {
			errs = append(errs, field.Duplicate(p.Child("name"), v.Name))
		}
		if v.HostPath != nil {
			errs = append(errs, field.Forbidden(p.Child("hostPath"), "The pod template cannot mount a host path."))
		}
	}
	return errs
}

// koodamVolumes refuses each mount of c on a Koodam volume. These volumes can hold the login,
// the gateway key and the SSH keys, so only the Koodam containers mount them.
func koodamVolumes(c corev1.Container, vols map[string]bool, path *field.Path) field.ErrorList {
	var errs field.ErrorList
	for i, m := range c.VolumeMounts {
		if vols[m.Name] {
			errs = append(errs, field.Forbidden(path.Child("volumeMounts").Index(i).Child("name"),
				"Koodam owns the volume "+m.Name+"."))
		}
	}
	return errs
}

// hostAccess refuses the container fields that can give access to the node: securityContext and
// hostPort. The pod securityContext of Koodam then applies to each container.
func hostAccess(c corev1.Container, path *field.Path) field.ErrorList {
	var errs field.ErrorList
	if c.SecurityContext != nil {
		errs = append(errs, field.Forbidden(path.Child("securityContext"), "Koodam owns this field."))
	}
	for i, port := range c.Ports {
		if port.HostPort != 0 {
			errs = append(errs, field.Forbidden(path.Child("ports").Index(i).Child("hostPort"),
				"The pod template cannot use a host port."))
		}
	}
	return errs
}

// ownedOrder gives the owned pod fields in a fixed order, so the errors have a fixed order.
var ownedOrder = []string{"automountServiceAccountToken", "enableServiceLinks", "securityContext",
	"serviceAccountName", "restartPolicy", "activeDeadlineSeconds", "hostNetwork", "hostPID", "hostIPC",
	"hostUsers", "shareProcessNamespace", "ephemeralContainers"}

// validateMain refuses the fields of the user main container u that are not in the permitted
// list, and the env names and mount paths that the Koodam main container c has.
func validateMain(c *corev1.Container, u corev1.Container, path *field.Path) field.ErrorList {
	var errs field.ErrorList
	rest := u.DeepCopy()
	rest.Image, rest.Command, rest.Args, rest.WorkingDir = "", nil, nil, ""
	rest.Env, rest.EnvFrom, rest.VolumeMounts, rest.Resources = nil, nil, nil, corev1.ResourceRequirements{}
	if !equality.Semantic.DeepEqual(*rest, corev1.Container{Name: u.Name}) {
		errs = append(errs, field.Forbidden(path, "The main container accepts only image, command, args, "+
			"workingDir, env, envFrom, resources and volumeMounts. Koodam owns the other fields."))
	}
	if c == nil {
		return errs
	}
	for i, e := range u.Env {
		for _, ce := range c.Env {
			if e.Name == ce.Name {
				errs = append(errs, field.Duplicate(path.Child("env").Index(i).Child("name"), e.Name))
			}
		}
	}
	for i, m := range u.VolumeMounts {
		for _, cm := range c.VolumeMounts {
			p := path.Child("volumeMounts").Index(i)
			if inside(m.MountPath, cm.MountPath) || inside(cm.MountPath, m.MountPath) {
				errs = append(errs, field.Invalid(p.Child("mountPath"), m.MountPath,
					fmt.Sprintf("Koodam mounts %s.", cm.MountPath)))
			}
		}
	}
	return errs
}

// inside is true if path p is dir or a path in dir. It cleans the 2 paths first, because the
// container runtime also cleans them: "/home//koodam" and "/home/x/../koodam" are "/home/koodam".
func inside(p, dir string) bool {
	p, dir = pathpkg.Clean("/"+p), pathpkg.Clean("/"+dir)
	return p == dir || dir == "/" || strings.HasPrefix(p, dir+"/")
}

// reservedKeys refuses each key with ReservedPrefix or with a subdomain of it, for example
// core.koodam.dev/.
func reservedKeys(m map[string]string, path *field.Path) field.ErrorList {
	var errs field.ErrorList
	for k := range m {
		prefix, _, named := strings.Cut(k, "/")
		if named && (prefix+"/" == ReservedPrefix || strings.HasSuffix(prefix, "."+strings.TrimSuffix(ReservedPrefix, "/"))) {
			errs = append(errs, field.Forbidden(path.Key(k),
				"Koodam owns the keys with the prefix "+ReservedPrefix+" or a subdomain of it."))
		}
	}
	return errs
}

// mergedResources returns the Koodam list k with the values of the user list u on top.
func mergedResources(k, u corev1.ResourceList) corev1.ResourceList {
	if len(u) == 0 {
		return k
	}
	m := make(corev1.ResourceList, len(k)+len(u))
	maps.Copy(m, k)
	maps.Copy(m, u)
	return m
}

// names returns the names of the containers in each list.
func names(lists ...[]corev1.Container) map[string]bool {
	m := map[string]bool{}
	for _, l := range lists {
		for _, c := range l {
			m[c.Name] = true
		}
	}
	return m
}

// find returns the container with the name, or nil.
func find(cs []corev1.Container, name string) *corev1.Container {
	for i := range cs {
		if cs[i].Name == name {
			return &cs[i]
		}
	}
	return nil
}

// merged returns the user map with the base map on top. Validate refuses a user key that base
// can have, so no user value is lost.
func merged(user, base map[string]string) map[string]string {
	if len(user) == 0 && len(base) == 0 {
		return nil
	}
	m := make(map[string]string, len(user)+len(base))
	maps.Copy(m, user)
	maps.Copy(m, base)
	return m
}
