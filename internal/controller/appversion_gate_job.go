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
	"crypto/sha256"
	"fmt"
	"slices"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"

	corev1beta1 "github.com/mohanraj00/koodam/api/v1beta1"
	"github.com/mohanraj00/koodam/internal/evidence"
)

// The gate Job of an AppVersion (docs/evidence-gate.md). The gate uses the Job verbs and the
// pod list verb that the controller ServiceAccount has from the base role. It adds no RBAC verb.
const (
	// gateLabel is the label of the gate Job and its pod, with the version name. The pod does not
	// have the labels of the version, so it is not behind the Service of the version.
	gateLabel = "koodam.dev/app-gate"
	// gateRunContainer is the init container that runs nookku gate run.
	gateRunContainer = "gate"
	// gateVerifyContainer is the container that runs nookku gate verify and writes the report.
	gateVerifyContainer = "verify"
	// gateWorkDir has the copy of the project, with the tests and the gate folder.
	gateWorkDir = "/koodam/gate"
	// gateHomeDir is NOOKKU_HOME, with the copies of the seals.
	gateHomeDir = "/koodam/nookku-home"
	// gateHomeEnv is the env of Nookku for the folder with the copies of the seals.
	gateHomeEnv = "NOOKKU_HOME"
	// gateTargetEnv is the env with the address of the Service of the version: a host and a port.
	gateTargetEnv = "KOODAM_GATE_TARGET"
	// gateReportPath is the termination message file of the verify container.
	gateReportPath = "/dev/termination-log"
	// gateBackoffLimit is the number of retries of the gate pod. Each pod has new volumes.
	gateBackoffLimit = 2
	// gateDeadlineSeconds is the deadline of the gate Job if the spec has none. It is the default
	// of the CRD.
	gateDeadlineSeconds = 1800
)

// gateName is the name of the gate Job. The pods of a Job have its name as a label value, so the
// name has 63 characters or fewer. A long version name gets a short form with a hash of the name.
func gateName(av *corev1beta1.AppVersion) string {
	const suffix, hashLen = "-gate", 8
	name := appVersionName(av)
	if len(name)+len(suffix) <= validation.DNS1123LabelMaxLength {
		return name + suffix
	}
	sum := sha256.Sum256([]byte(av.Name))
	keep := validation.DNS1123LabelMaxLength - len(suffix) - hashLen - 1
	return fmt.Sprintf("%s-%x%s", name[:keep], sum[:hashLen/2], suffix)
}

// gateTarget is the address of the Service of the version for the entries of the gate project.
func gateTarget(av *corev1beta1.AppVersion, deploy *appsv1.Deployment) string {
	port := appContainer(&deploy.Spec.Template.Spec).Ports[0].ContainerPort
	return fmt.Sprintf("%s.%s.svc:%d", appVersionName(av), av.Namespace, port)
}

// gatePod is the pod of the gate Job. It has no ServiceAccount token, no gateway key and no
// Secret. The two containers have a fixed argv: no value of the spec goes through a shell. The
// verify container mounts the two volumes read-only.
func gatePod(av *corev1beta1.AppVersion, target, subject string) corev1.PodSpec {
	const work, home = "gate-work", "nookku-home"
	g, project := av.Spec.Gate.Nookku, gateWorkDir+"/project"
	container := func(name string, readOnly bool, env []corev1.EnvVar, argv ...string) corev1.Container {
		return corev1.Container{Name: name, Image: g.Image, Command: argv,
			Env: append([]corev1.EnvVar{{Name: gateHomeEnv, Value: gateHomeDir}}, env...),
			VolumeMounts: []corev1.VolumeMount{{Name: work, MountPath: gateWorkDir, ReadOnly: readOnly},
				{Name: home, MountPath: gateHomeDir, ReadOnly: readOnly}},
			SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: new(false),
				Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{capabilityAll}}}}
	}
	empty := corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}
	return corev1.PodSpec{
		RestartPolicy:                corev1.RestartPolicyNever,
		AutomountServiceAccountToken: new(false),
		EnableServiceLinks:           new(false),
		InitContainers: []corev1.Container{container(gateRunContainer, false,
			[]corev1.EnvVar{{Name: gateTargetEnv, Value: target}},
			"python3", "-c", evidence.RunScript, g.Root, project, g.GateFile, subject)},
		Containers: []corev1.Container{container(gateVerifyContainer, true, nil,
			"python3", "-c", evidence.VerifyScript, project, gateReportPath)},
		Volumes: []corev1.Volume{{Name: work, VolumeSource: empty}, {Name: home, VolumeSource: empty}},
	}
}

func gateJob(av *corev1beta1.AppVersion, target, subject string) *batchv1.Job {
	labels := map[string]string{gateLabel: av.Name}
	m := appVersionMeta(av)
	m.Name, m.Labels = gateName(av), labels
	deadline := int64(gateDeadlineSeconds)
	if d := av.Spec.Gate.Nookku.DeadlineSeconds; d != nil {
		deadline = int64(*d)
	}
	return &batchv1.Job{ObjectMeta: m, Spec: batchv1.JobSpec{
		BackoffLimit:          new(int32(gateBackoffLimit)),
		ActiveDeadlineSeconds: &deadline,
		Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels},
			Spec: gatePod(av, target, subject)},
	}}
}

// isGatePod is true if the pod has the two containers of the gate Job of this version: the same
// names, images, argv and env. The subject in the argv of the pod is not a part of the compare,
// because the check compares the subject of the report with the subject of the version.
func isGatePod(got *corev1.PodSpec, av *corev1beta1.AppVersion, target string) bool {
	if len(got.InitContainers) != 1 || len(got.Containers) != 1 || len(got.EphemeralContainers) != 0 ||
		len(got.InitContainers[0].Command) == 0 {
		return false
	}
	argv := got.InitContainers[0].Command
	want := gatePod(av, target, argv[len(argv)-1])
	same := func(a, b *corev1.Container) bool {
		return a.Name == b.Name && a.Image == b.Image && slices.Equal(a.Command, b.Command) && len(a.Args) == 0 &&
			slices.Equal(a.Env, b.Env) && len(a.EnvFrom) == 0
	}
	return same(&got.InitContainers[0], &want.InitContainers[0]) && same(&got.Containers[0], &want.Containers[0])
}
