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

package chart

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/yaml"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
)

const (
	quarantineLabel   = "koodam.dev/quarantine"
	quarantinePolicy  = "koodam-quarantine"
	podLabelsPolicy   = "koodam-pod-labels"
	managerUser       = "system:serviceaccount:koodam-system:koodam-controller-manager"
	policyKind        = "ValidatingAdmissionPolicy"
	policyBindingKind = "ValidatingAdmissionPolicyBinding"
)

// quarantineObjects returns the rendered policies and bindings of a quarantine, by kind and name.
func quarantineObjects(t *testing.T, values string) map[string]*unstructured.Unstructured {
	t.Helper()
	out, err := helmTemplate(t, values)
	if err != nil {
		t.Fatalf("helm template failed: %v\n%s", err, out)
	}
	got := map[string]*unstructured.Unstructured{}
	for _, kind := range []string{policyKind, policyBindingKind} {
		for _, doc := range objects(t, out, kind) {
			u := &unstructured.Unstructured{}
			if err := yaml.Unmarshal([]byte(doc), &u.Object); err != nil {
				t.Fatal(err)
			}
			if u.GetName() == quarantinePolicy || u.GetName() == podLabelsPolicy {
				got[kind+"/"+u.GetName()] = u
			}
		}
	}
	if len(got) != 4 {
		t.Fatalf("values %q: got %d quarantine policies and bindings, want 4", values, len(got))
	}
	return got
}

// TestQuarantinePolicyRender proves that each install has the 2 policies of a quarantine, that
// the only exception is the manager service account of the install, and that zone.namespaces
// limits the bindings.
func TestQuarantinePolicyRender(t *testing.T) {
	exception := func(objs map[string]*unstructured.Unstructured) string {
		vars, _, _ := unstructured.NestedSlice(objs[policyKind+"/"+quarantinePolicy].Object, "spec", "variables")
		for _, v := range vars {
			if m := v.(map[string]any); m["name"] == "manager" {
				return m["expression"].(string)
			}
		}
		return ""
	}
	objs := quarantineObjects(t, "")
	if got, want := exception(objs), `request.userInfo.username == "`+managerUser+`"`; got != want {
		t.Errorf("the exception is %q, want %q", got, want)
	}
	for name, u := range objs {
		text, err := yaml.Marshal(u.Object)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(text), "namespaceSelector") {
			t.Errorf("%s has a namespace selector with no zone.namespaces", name)
		}
		// Only the variable "manager" can name a user. Only the label rule and the finalizer rule
		// read it, so the manager has no exception for a delete.
		if n := strings.Count(string(text), "userInfo"); n != 0 && name != policyKind+"/"+quarantinePolicy || n > 1 {
			t.Errorf("%s reads the user of the request %d times", name, n)
		}
		if n := strings.Count(string(text), "variables.manager"); n > 2 {
			t.Errorf("%s has %d rules with the exception, want 2", name, n)
		}
		if strings.Contains(string(text), "failurePolicy: Ignore") || strings.Contains(string(text), "Warn") ||
			strings.Contains(string(text), "Audit") {
			t.Errorf("%s does not refuse the request", name)
		}
	}

	scoped := quarantineObjects(t, "serviceAccount: {name: custom-sa}\nzone: {name: zone-a, namespaces: [team-1]}\n")
	want := `request.userInfo.username == "system:serviceaccount:koodam-system:custom-sa"`
	if got := exception(scoped); got != want {
		t.Errorf("the exception is %q, want %q", got, want)
	}
	for _, name := range []string{quarantinePolicy, podLabelsPolicy} {
		exprs, _, _ := unstructured.NestedSlice(scoped[policyBindingKind+"/"+name].Object,
			"spec", "matchResources", "namespaceSelector", "matchExpressions")
		if len(exprs) != 1 {
			t.Fatalf("the binding %s has %d namespace expressions, want 1", name, len(exprs))
		}
		e := exprs[0].(map[string]any)
		values, _ := yaml.Marshal(e["values"])
		if e["key"] != "kubernetes.io/metadata.name" || e["operator"] != "In" ||
			string(values) != "- koodam-system\n- team-1\n" {
			t.Errorf("the binding %s is not for the namespaces of the install: %v", name, e)
		}
	}
}

// TestQuarantinePolicyRules applies the rendered policies to an API server and proves each rule
// with 2 users: an admin who is not the manager, and the manager service account of the install.
func TestQuarantinePolicyRules(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("KUBEBUILDER_ASSETS is not set")
		}
		t.Skip("KUBEBUILDER_ASSETS is not set. Run make test.")
	}
	objs := quarantineObjects(t, "")
	admin, manager := quarantineClients(t)
	ctx := context.Background()
	for _, u := range objs {
		if err := admin.Create(ctx, u); err != nil {
			t.Fatal(err)
		}
	}

	const ns = "default"
	meta := func(name string, labels map[string]string) metav1.ObjectMeta {
		return metav1.ObjectMeta{Name: name, Namespace: ns, Labels: labels}
	}
	podSpec := corev1.PodSpec{RestartPolicy: corev1.RestartPolicyNever,
		Containers: []corev1.Container{{Name: "c", Image: "example.com/quarantine:test"}}}
	template := corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "q"}}, Spec: podSpec}
	selector := &metav1.LabelSelector{MatchLabels: map[string]string{"app": "q"}}
	always := template.DeepCopy()
	always.Spec.RestartPolicy = corev1.RestartPolicyAlways
	pod := &corev1.Pod{ObjectMeta: meta("run", map[string]string{"koodam.dev/agent": "run", "other": "a"}), Spec: podSpec}
	plain := &corev1.Pod{ObjectMeta: meta("plain", nil), Spec: podSpec}
	agent := &corev1alpha1.Agent{ObjectMeta: meta("run", nil), Spec: corev1alpha1.AgentSpec{
		Image: "example.com/quarantine:test", Command: []string{"/run"}, Model: "m", Budget: resource.MustParse("1"),
		DeadlineSeconds: 600}}
	// The koodam finalizer keeps the Agent after a permitted delete.
	agent.Finalizers = []string{"example.com/other", "koodam.dev/revoke"}
	children := []client.Object{
		pod,
		&batchv1.Job{ObjectMeta: meta("kd-run", nil), Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: podSpec}}},
		&appsv1.StatefulSet{ObjectMeta: meta("ws", nil), Spec: appsv1.StatefulSetSpec{Selector: selector, Template: *always}},
		&appsv1.Deployment{ObjectMeta: meta("kd-v1", nil),
			Spec: appsv1.DeploymentSpec{Selector: selector, Template: *always}},
		&corev1.PersistentVolumeClaim{ObjectMeta: meta("home-ws-0", nil), Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")}}}},
	}
	for _, o := range append([]client.Object{plain, agent}, children...) {
		if err := admin.Create(ctx, o); err != nil {
			t.Fatalf("create %T: %v", o, err)
		}
	}
	patch := func(c client.Client, o client.Object, labels string, sub ...string) error {
		p := client.RawPatch(types.MergePatchType, []byte(`{"metadata":{"labels":`+labels+`}}`))
		if len(sub) > 0 {
			return c.SubResource(sub[0]).Patch(ctx, o, p)
		}
		return c.Patch(ctx, o, p)
	}
	const on, off = `{"` + quarantineLabel + `":"true"}`, `{"` + quarantineLabel + `":null}`
	refused := func(name string, err error) {
		t.Helper()
		if !apierrors.IsForbidden(err) {
			t.Errorf("%s: got %v, want a refusal of the policy", name, err)
		}
	}
	// kept is for a status update. The API server refuses it, or the kind ignores a label change
	// in a status update. In the two cases, the object keeps the label.
	kept := func(name string, o client.Object, err error) {
		t.Helper()
		if err != nil && !apierrors.IsForbidden(err) {
			t.Errorf("%s: got %v, want a refusal of the policy", name, err)
		}
		if err := admin.Get(ctx, client.ObjectKeyFromObject(o), o); err != nil || o.GetLabels()[quarantineLabel] != "true" {
			t.Errorf("%s: the object does not have the label: %v", name, err)
		}
	}
	permitted := func(name string, err error) {
		t.Helper()
		if err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}

	// The API server starts to enforce a new policy after a short time.
	deadline := time.Now().Add(2 * time.Minute)
	for {
		labelErr := patch(admin, plain, on)
		identityErr := patch(admin, pod, `{"koodam.dev/agent":"probe"}`)
		if apierrors.IsForbidden(labelErr) && apierrors.IsForbidden(identityErr) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the API server does not enforce the policies: %v, %v", labelErr, identityErr)
		}
		if labelErr == nil {
			permitted("the reset of the probe label", patch(manager, plain, off))
		}
		if identityErr == nil {
			permitted("the reset of the probe pod", patch(admin, pod, `{"koodam.dev/agent":"run"}`))
		}
		time.Sleep(time.Second)
	}

	// Only the manager adds the label.
	refused("a new pod with the label", admin.Create(ctx,
		&corev1.Pod{ObjectMeta: meta("born", map[string]string{quarantineLabel: "true"}), Spec: podSpec}))
	refused("the label on the Agent by the admin", patch(admin, agent, on))
	permitted("the label on the Agent by the manager", patch(manager, agent, on))
	for _, o := range children {
		refused("the label by the admin", patch(admin, o, on))
		permitted("the label by the manager", patch(manager, o, on))
	}

	// The children stay, and the label stays. The manager has no exception for a delete.
	for _, o := range children {
		kind := typeName(o)
		refused("the delete of a "+kind, admin.Delete(ctx, o))
		refused("the delete of a "+kind+" by the manager", manager.Delete(ctx, o))
		refused("the delete of a "+kind+" with no grace", admin.Delete(ctx, o, client.GracePeriodSeconds(0)))
		refused("the removal of the label of a "+kind, patch(admin, o, off))
		refused("a new value of the label of a "+kind, patch(admin, o, `{"`+quarantineLabel+`":"false"}`))
		kept("the removal of the label through the status of a "+kind, o, patch(admin, o, off, "status"))
		permitted("a different label on a "+kind, patch(admin, o, `{"note":"kept"}`))
	}
	refused("a delete of each pod", admin.DeleteAllOf(ctx, &corev1.Pod{}, client.InNamespace(ns),
		client.MatchingLabels{"koodam.dev/agent": "run"}))

	// The workload: no label change, no removal of a koodam finalizer, no Foreground delete. A
	// normal delete is permitted.
	finalizers := func(c client.Client, list string) error {
		return c.Patch(ctx, agent, client.RawPatch(types.MergePatchType, []byte(`{"metadata":{"finalizers":`+list+`}}`)))
	}
	refused("the removal of a koodam finalizer", finalizers(admin, `["example.com/other"]`))
	refused("the removal of each finalizer", finalizers(admin, `null`))
	permitted("the removal of a different finalizer", finalizers(admin, `["koodam.dev/revoke"]`))
	refused("the removal of the label of the Agent", patch(admin, agent, off))
	kept("the removal of the label through the status of the Agent", agent, patch(admin, agent, off, "status"))
	refused("the Foreground delete of the Agent",
		admin.Delete(ctx, agent, client.PropagationPolicy(metav1.DeletePropagationForeground)))
	permitted("the normal delete of the Agent", admin.Delete(ctx, agent))

	// The koodam labels of a pod are immutable, also for the manager and through the status.
	for name, labels := range map[string]string{
		"a new value":     `{"koodam.dev/agent":"other"}`,
		"the removal":     `{"koodam.dev/agent":null}`,
		"a second label":  `{"koodam.dev/workspace":"ws"}`,
		"an empty value":  `{"koodam.dev/agent":""}`,
		"a version label": `{"koodam.dev/app":"a","koodam.dev/app-version":"v1"}`,
	} {
		refused(name+" of a koodam label", patch(admin, pod, labels))
		refused(name+" of a koodam label by the manager", patch(manager, pod, labels))
		refused(name+" of a koodam label through the status", patch(admin, pod, labels, "status"))
	}
	permitted("a different label on a koodam pod", patch(admin, pod, `{"other":"b"}`))
	permitted("a koodam label on a pod with none", patch(admin, plain, `{"koodam.dev/agent":"run"}`))

	// After the release, the manager removes the label and the finalizer, and the delete is
	// permitted.
	permitted("the removal of the finalizer by the manager", finalizers(manager, `null`))
	for _, o := range children {
		permitted("the removal of the label by the manager", patch(manager, o, off))
		permitted("the delete after the release", admin.Delete(ctx, o))
	}
}

// quarantineClients starts an envtest API server with the CRDs. It returns a client of an admin,
// and a client of a user with the name of the manager service account of the default install.
func quarantineClients(t *testing.T) (admin, manager client.Client) {
	t.Helper()
	// envtest waits only 5 seconds for the default namespace. On a host with a high load, the
	// first start can be too slow, so the test tries 3 times.
	var env *envtest.Environment
	var cfg *rest.Config
	for try := 1; ; try++ {
		env = &envtest.Environment{CRDDirectoryPaths: []string{filepath.Join("..", "..", "config", "crd", "bases")},
			ErrorIfCRDPathMissing: true}
		var err error
		if cfg, err = env.Start(); err == nil {
			break
		}
		_ = env.Stop()
		if try == 3 {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { _ = env.Stop() })
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	admin, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	user, err := env.AddUser(envtest.User{Name: managerUser, Groups: []string{"system:masters"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	manager, err = client.New(user.Config(), client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	return admin, manager
}

// typeName is the kind of a child, for the name of a check.
func typeName(o client.Object) string {
	switch o.(type) {
	case *corev1.Pod:
		return "pod"
	case *batchv1.Job:
		return "job"
	case *appsv1.StatefulSet:
		return "statefulset"
	case *appsv1.Deployment:
		return "deployment"
	default:
		return "pvc"
	}
}
