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
	corev1 "k8s.io/api/core/v1"

	"github.com/mohanraj00/koodam/internal/agentingress"
)

// targetCase changes the fake world before the first read of a resolve.
type targetCase struct {
	name   string
	change func(*targetReader)
}

func setIP(ip string) func(*targetReader) {
	return func(f *targetReader) { f.pod().Status.PodIP = ip }
}

func mainPort(f *targetReader) *corev1.ContainerPort { return &f.pod().Spec.Containers[0].Ports[0] }

func httpPort() corev1.ContainerPort {
	return corev1.ContainerPort{Name: agentingress.ReservedPortName, ContainerPort: tgPort}
}

func initWith(ports ...corev1.ContainerPort) []corev1.Container {
	return []corev1.Container{{Name: tgInit, Ports: ports}}
}
