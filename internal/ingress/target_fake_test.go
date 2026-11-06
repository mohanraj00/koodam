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
	"net/netip"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
	"github.com/mohanraj00/koodam/internal/agentingress"
)

// The fixtures have only public fake names, UIDs, ports, images and status. They have no env,
// token, Secret, key alias or key hash.
const (
	tgNamespace      = "runs"
	tgOtherNamespace = "runs-b"
	tgAgentName      = "web"
	tgJobName        = "kd-web"
	tgPodName        = "kd-web-x7k2p"
	tgSecondPodName  = "kd-web-q4m9z"
	tgPolicyName     = "koodam-agent"
	tgProxyNamespace = "koodam-system"
	tgRelease        = "koodam"
	tgPort           = int32(8080)
	tgPodIP          = "10.244.1.7"
	tgPortSuffix     = ":8080"
	tgAddr           = tgPodIP + tgPortSuffix
	tgOther          = "other"
	tgOtherJob       = "kd-other"
	tgNameKey        = "app.kubernetes.io/name"
	tgInstanceKey    = "app.kubernetes.io/instance"
	tgComponentKey   = "app.kubernetes.io/component"

	tgAgentUID     types.UID = "00000000-0000-4000-8000-000000000001"
	tgJobUID       types.UID = "00000000-0000-4000-8000-000000000002"
	tgPodUID       types.UID = "00000000-0000-4000-8000-000000000003"
	tgSecondPodUID types.UID = "00000000-0000-4000-8000-000000000004"
	tgOtherUID     types.UID = "00000000-0000-4000-8000-000000000009"
)

// The fixed name of a fake read. A failure message never contains an object, an error or an
// address.
const (
	opOther = "unexpected read"
)

// targetReader is a local fake client.Reader. It keeps the fixed names of the reads only in memory
// in this process. It has no write method and no object: it records each read and refuses it. The
// constructor tests use it to prove that the constructor reads no object.
type targetReader struct {
	ctx context.Context // Each read must get this context.
	ops []string
}

// newTargetWorld returns a fake with no object.
func newTargetWorld(ctx context.Context) *targetReader {
	return &targetReader{ctx: ctx}
}

func (f *targetReader) Get(context.Context, client.ObjectKey, client.Object, ...client.GetOption) error {
	f.ops = append(f.ops, opOther)
	return apierrors.NewNotFound(schema.GroupResource{}, "")
}

func (f *targetReader) List(context.Context, client.ObjectList, ...client.ListOption) error {
	f.ops = append(f.ops, opOther)
	return apierrors.NewBadRequest("")
}

var _ client.Reader = (*targetReader)(nil)

func tgController(apiVersion, kind, name string, uid types.UID) metav1.OwnerReference {
	return metav1.OwnerReference{APIVersion: apiVersion, Kind: kind, Name: name, UID: uid,
		Controller: new(true), BlockOwnerDeletion: new(true)}
}

func tgDeleting() *metav1.Time { return new(metav1.Unix(1, 0)) }

func tgAgent() *corev1alpha1.Agent {
	return &corev1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: tgAgentName, Namespace: tgNamespace, UID: tgAgentUID},
		Spec: corev1alpha1.AgentSpec{Image: "example.test/agent:1", Command: []string{"serve"},
			Model: "test-model", Budget: resource.MustParse("1"), IngressPort: new(tgPort)},
		Status: corev1alpha1.AgentStatus{Phase: corev1alpha1.AgentRunning, Job: tgJobName},
	}
}

func tgJob() *batchv1.Job {
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: tgJobName, Namespace: tgNamespace, UID: tgJobUID,
			Labels: map[string]string{targetAgentLabel: tgAgentName},
			OwnerReferences: []metav1.OwnerReference{
				tgController("core.koodam.dev/v1alpha1", agentKind, tgAgentName, tgAgentUID)}},
		Status: batchv1.JobStatus{Active: 1},
	}
}

func tgPod(name string, uid types.UID) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: tgNamespace, UID: uid,
			Labels: map[string]string{targetAgentLabel: tgAgentName, targetRunLabel: tgJobName,
				"batch.kubernetes.io/job-name": tgJobName},
			OwnerReferences: []metav1.OwnerReference{tgController("batch/v1", jobKind, tgJobName, tgJobUID)}},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name: targetContainer, Image: "example.test/agent:1",
			Ports: []corev1.ContainerPort{{Name: agentingress.ReservedPortName, ContainerPort: tgPort,
				Protocol: corev1.ProtocolTCP}},
		}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning, PodIP: tgPodIP,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}},
	}
}

func tgProxyLabels() map[string]string {
	return map[string]string{tgNameKey: tgRelease, tgInstanceKey: tgRelease,
		tgComponentKey: agentingress.ProxyComponent}
}

func tgGuard(namespace string) agentingress.PolicyGuard {
	return agentingress.PolicyGuard{Namespace: namespace, Name: tgPolicyName, ProxyNamespace: tgProxyNamespace,
		ProxyLabels: tgProxyLabels()}
}

// tgResolver returns a resolver of f with the guards of the two run namespaces.
func tgResolver(t *testing.T, f *targetReader) *targetResolver {
	t.Helper()
	r, err := newTargetResolver(f, []agentingress.PolicyGuard{tgGuard(tgNamespace), tgGuard(tgOtherNamespace)})
	if err != nil || r == nil {
		t.Fatal("newTargetResolver refused a valid configuration")
	}
	return r
}

func tgWant() target {
	return target{namespace: tgNamespace, agent: tgAgentName, agentUID: tgAgentUID, job: tgJobName,
		jobUID: tgJobUID, pod: tgPodName, podUID: tgPodUID, addr: netip.MustParseAddrPort(tgAddr)}
}
