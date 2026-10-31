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

// Package agentingress has the pure checks of the traced Agent ingress of #147. The admission
// validator uses them. Later, the controller and the ingress proxy use them as a second defense.
package agentingress

import (
	"errors"

	corev1 "k8s.io/api/core/v1"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
)

// ReservedPortName is the container port name that the ingress owns when spec.ingressPort is set.
const ReservedPortName = "http"

// The errors have fixed text. They never contain a value from the Agent.
var (
	ErrPortRange    = errors.New("spec.ingressPort must be from 1 to 65535")
	ErrReservedPort = errors.New("spec.podTemplate cannot have a container or init container port " +
		"with the name http when spec.ingressPort is set")
)

// Check returns a fixed error if the ingress fields of spec are not valid. If spec.ingressPort is
// not set, a legacy Agent can use the port name http. If it is set, it must be from 1 to 65535, and
// no container or init container, also the main container, can have a port with the name http.
func Check(spec corev1alpha1.AgentSpec) error {
	if spec.IngressPort == nil {
		return nil
	}
	if p := *spec.IngressPort; p < 1 || p > 65535 {
		return ErrPortRange
	}
	if spec.PodTemplate == nil {
		return nil
	}
	s := spec.PodTemplate.Spec
	if reserved(s.Containers) || reserved(s.InitContainers) {
		return ErrReservedPort
	}
	return nil
}

// reserved is true if a container in cs has a port with ReservedPortName.
func reserved(cs []corev1.Container) bool {
	for _, c := range cs {
		for _, p := range c.Ports {
			if p.Name == ReservedPortName {
				return true
			}
		}
	}
	return false
}
