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

package podtemplate

import (
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

// ReservedSecretPrefix is the name prefix of the Secrets that hold the gateway key of an Agent
// run. A pod template cannot refer to these Secrets, thus a run cannot read the key of a
// different run in the same namespace.
const ReservedSecretPrefix = "kd-"

// reservedSecrets refuses each reference to a Secret with ReservedSecretPrefix in the volumes,
// the projected volumes, env and envFrom of each container and init container.
func reservedSecrets(s *corev1.PodSpec, path *field.Path) field.ErrorList {
	var errs field.ErrorList
	check := func(name string, p *field.Path) {
		if strings.HasPrefix(name, ReservedSecretPrefix) {
			errs = append(errs, field.Forbidden(p,
				"The pod template cannot refer to a Secret with the prefix "+ReservedSecretPrefix+"."))
		}
	}
	for i, v := range s.Volumes {
		p := path.Child("volumes").Index(i)
		if v.Secret != nil {
			check(v.Secret.SecretName, p.Child("secret", "secretName"))
		}
		if v.Projected != nil {
			for j, src := range v.Projected.Sources {
				if src.Secret != nil {
					check(src.Secret.Name, p.Child("projected", "sources").Index(j).Child("secret", "name"))
				}
			}
		}
	}
	containers := func(cs []corev1.Container, p *field.Path) {
		for i, c := range cs {
			cp := p.Index(i)
			for j, e := range c.Env {
				if e.ValueFrom != nil && e.ValueFrom.SecretKeyRef != nil {
					check(e.ValueFrom.SecretKeyRef.Name,
						cp.Child("env").Index(j).Child("valueFrom", "secretKeyRef", "name"))
				}
			}
			for j, e := range c.EnvFrom {
				if e.SecretRef != nil {
					check(e.SecretRef.Name, cp.Child("envFrom").Index(j).Child("secretRef", "name"))
				}
			}
		}
	}
	containers(s.InitContainers, path.Child("initContainers"))
	containers(s.Containers, path.Child("containers"))
	return errs
}
