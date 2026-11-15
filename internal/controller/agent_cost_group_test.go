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
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
	"github.com/mohanraj00/koodam/internal/gateway"
)

// The cost groups of the tests. The Profile of a test permits only the first one.
const (
	testCostGroupAllowed = "project-a"
	testCostGroupDenied  = "ops"
)

// costGroupGateway checks the saved status at CreateKey. It keeps no key value.
type costGroupGateway struct {
	*countGateway
	reader   client.Reader
	agent    types.NamespacedName
	group    string
	calls    int
	saved    bool
	metadata map[string]string
	err      error
}

func (g *costGroupGateway) CreateKey(ctx context.Context, s gateway.KeySpec) error {
	a := &corev1alpha1.Agent{}
	g.saved = g.reader.Get(ctx, g.agent, a) == nil && a.Status.CostGroup == g.group &&
		a.Status.KeyHash == gateway.HashKey(s.Value) && a.Status.KeyAlias == s.Alias
	g.calls++
	g.metadata = s.Metadata
	return g.err
}

var _ = Describe("Agent cost group", func() {
	DescribeTable("checks the label before it makes a key", func(label *string, bound bool, message string) {
		ctx := context.Background()
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "cost-agent-"}}
		Expect(k8sClient.Create(ctx, ns)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(ctx, ns)).To(Succeed()) })
		p := &corev1alpha1.Profile{ObjectMeta: metav1.ObjectMeta{Name: ns.Name},
			Spec: corev1alpha1.ProfileSpec{Namespaces: []string{ns.Name}, CostGroups: []string{testCostGroupAllowed}}}
		if bound {
			Expect(k8sClient.Create(ctx, p)).To(Succeed())
			DeferCleanup(func() { Expect(k8sClient.Delete(ctx, p)).To(Succeed()) })
			ns.Labels = map[string]string{corev1alpha1.ProfileLabel: p.Name}
			Expect(k8sClient.Update(ctx, ns)).To(Succeed())
		}
		a := ingAgent()
		a.Namespace, a.UID = ns.Name, ""
		if label != nil {
			a.Labels = map[string]string{corev1alpha1.CostGroupLabel: *label}
		}
		Expect(k8sClient.Create(ctx, a)).To(Succeed())
		key := client.ObjectKeyFromObject(a)
		gw := &costGroupGateway{countGateway: &countGateway{}, reader: k8sClient, agent: key}
		if message == "" && label != nil {
			gw.group = *label
		}
		r := &AgentReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), APIReader: k8sClient,
			Gateway: gw, GatewayURL: testGatewayURL, Grace: time.Minute}
		DeferCleanup(func() {
			Expect(k8sClient.Delete(ctx, a)).To(Succeed())
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())
		})
		_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient.Get(ctx, key, a)).To(Succeed())
		if message != "" {
			Expect(a.Status.Phase).To(Equal(corev1alpha1.AgentFailed))
			c := meta.FindStatusCondition(a.Status.Conditions, condProvisioned)
			Expect(c).NotTo(BeNil())
			Expect(c.Status).To(Equal(metav1.ConditionFalse))
			Expect(c.Reason).To(Equal(reasonCostGroupNotAllowed))
			Expect(c.Message).To(Equal(strings.ReplaceAll(message, "<namespace>", ns.Name)))
			Expect(gw.calls).To(BeZero())
			Expect(a.Finalizers).NotTo(ContainElement(finalizer))
			Expect(a.Status.KeyHash).To(BeEmpty())
			for _, obj := range []client.Object{&corev1.Secret{}, &batchv1.Job{}} {
				Expect(apierrors.IsNotFound(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns.Name, Name: runName(a)}, obj))).To(BeTrue())
			}
			return
		}
		Expect(a.Status.Phase).To(Equal(corev1alpha1.AgentRunning))
		Expect(a.Status.CostGroup).To(Equal(gw.group))
		Expect(gw.calls).To(Equal(1))
		Expect(gw.saved).To(BeTrue(), "the status must have the group before CreateKey")
		if gw.group == "" {
			Expect(gw.metadata).NotTo(HaveKey("costGroup"))
		} else {
			Expect(gw.metadata).To(HaveKeyWithValue("costGroup", gw.group))
			a.Labels[corev1alpha1.CostGroupLabel] = testCostGroupDenied
			Expect(k8sClient.Update(ctx, a)).To(Succeed())
			p.Spec.CostGroups = nil
			Expect(k8sClient.Update(ctx, p)).To(Succeed())
			_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())
			Expect(k8sClient.Get(ctx, key, a)).To(Succeed())
			Expect(a.Status.CostGroup).To(Equal(gw.group))
			Expect(a.Status.Phase).To(Equal(corev1alpha1.AgentRunning))
			Expect(gw.calls).To(Equal(1))
		}
	},
		Entry("no label", (*string)(nil), false, ""),
		Entry("empty label", new(""), false, ""),
		Entry("allowed label", new(testCostGroupAllowed), true, ""),
		Entry("value not permitted", new(testCostGroupDenied), true,
			`the label koodam.dev/cost-group has the value "ops", and Profile <namespace> does not permit it in spec.costGroups`),
		Entry("no bound Profile", new(testCostGroupDenied), false,
			"the label koodam.dev/cost-group needs a Profile bound to namespace <namespace>, with the value in spec.costGroups"),
	)
})

func TestCostGroupRetryKeepsSavedValue(t *testing.T) {
	for _, group := range []string{"", testCostGroupAllowed} {
		t.Run(group, func(t *testing.T) {
			a := ingAgent()
			a.Labels = map[string]string{corev1alpha1.CostGroupLabel: group}
			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: a.Namespace,
				Labels: map[string]string{corev1alpha1.ProfileLabel: "cost-profile"}}}
			p := &corev1alpha1.Profile{ObjectMeta: metav1.ObjectMeta{Name: "cost-profile"},
				Spec: corev1alpha1.ProfileSpec{Namespaces: []string{ns.Name}, CostGroups: []string{testCostGroupAllowed}}}
			c := ingWorld(t, a, ns, p)
			gw := &costGroupGateway{countGateway: &countGateway{}, reader: c, agent: ingKey,
				group: group, err: errors.New("try again")}
			r := &AgentReconciler{Client: c, Scheme: c.Scheme(), APIReader: c, Gateway: gw, GatewayURL: testGatewayURL}
			if _, err := r.Reconcile(t.Context(), reconcile.Request{NamespacedName: ingKey}); err == nil || !gw.saved {
				t.Fatal("the first call did not save the cost group before the gateway error")
			}
			if err := c.Get(t.Context(), ingKey, a); err != nil {
				t.Fatal("the test could not read the Agent")
			}
			a.Labels[corev1alpha1.CostGroupLabel] = testCostGroupDenied
			if c.Update(t.Context(), a) != nil || c.Delete(t.Context(), p) != nil {
				t.Fatal("the test could not change the label and remove the Profile")
			}
			// The fake API does not copy StringData to Data as the API server does.
			sec := &corev1.Secret{}
			if c.Get(t.Context(), types.NamespacedName{Namespace: a.Namespace, Name: runName(a)}, sec) != nil {
				t.Fatal("the test could not read the Secret")
			}
			sec.Data = map[string][]byte{keyField: []byte(sec.StringData[keyField])}
			sec.StringData = nil
			if c.Update(t.Context(), sec) != nil {
				t.Fatal("the test could not update the Secret")
			}
			gw.err = nil
			ingReconcile(t, r)
			if gw.calls != 2 || !gw.saved || gw.metadata["costGroup"] != group ||
				ingLive(t, c).Status.CostGroup != group || ingLive(t, c).Status.Phase != corev1alpha1.AgentRunning {
				t.Fatal("the retry changed the saved cost group")
			}
			if _, present := gw.metadata["costGroup"]; present != (group != "") {
				t.Fatal("the retry added or removed the cost group metadata")
			}
		})
	}
}
