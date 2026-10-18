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
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-logr/logr/funcr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
	"github.com/mohanraj00/koodam/internal/gateway"
)

var _ = Describe("Agent printer columns", func() {
	It("shows the phase and the spend", func() {
		crd := &unstructured.Unstructured{}
		crd.SetGroupVersionKind(schema.GroupVersionKind{
			Group: "apiextensions.k8s.io", Version: "v1", Kind: "CustomResourceDefinition"})
		Expect(k8sClient.Get(context.Background(),
			types.NamespacedName{Name: "agents." + corev1alpha1.GroupVersion.Group}, crd)).To(Succeed())
		versions, _, err := unstructured.NestedSlice(crd.Object, "spec", "versions")
		Expect(err).NotTo(HaveOccurred())
		Expect(versions).To(HaveLen(1))
		cols, _, err := unstructured.NestedSlice(versions[0].(map[string]any), "additionalPrinterColumns")
		Expect(err).NotTo(HaveOccurred())
		paths := map[string]string{}
		for _, c := range cols {
			m := c.(map[string]any)
			paths[m["name"].(string)] = m["jsonPath"].(string)
		}
		Expect(paths).To(HaveKeyWithValue("Phase", ".status.phase"))
		Expect(paths).To(HaveKeyWithValue("Spend", ".status.usage.spend"))
	})
})

// liveSpendLog is one row of the LiteLLM /spend/logs response.
type liveSpendLog struct {
	Status           string  `json:"status"`
	PromptTokens     int64   `json:"prompt_tokens"`
	CompletionTokens int64   `json:"completion_tokens"`
	Spend            float64 `json:"spend"`
}

// This spec runs against a real LiteLLM proxy. Set LITELLM_URL and LITELLM_MASTER_KEY_FILE, and
// LITELLM_MODEL if the model is not local-agent. It checks that the Agent status agrees with the
// gateway log after the revoke.
var _ = Describe("Agent usage with a real gateway", func() {
	It("writes the gateway count to the status", func() {
		base, keyFile := os.Getenv("LITELLM_URL"), os.Getenv("LITELLM_MASTER_KEY_FILE")
		if base == "" || keyFile == "" {
			Skip("LITELLM_URL and LITELLM_MASTER_KEY_FILE are not set")
		}
		master, err := os.ReadFile(keyFile)
		Expect(err).NotTo(HaveOccurred())
		model := os.Getenv("LITELLM_MODEL")
		if model == "" {
			model = "local-agent"
		}
		gw := gateway.NewLiteLLM(base, strings.TrimSpace(string(master)))
		// The test gateway uses proxy_batch_write_at: 1, as in the stage 2 spike.
		gw.SpendPoll, gw.SpendSettle, gw.SpendWait = time.Second, 3*time.Second, 20*time.Second

		logs := &bytes.Buffer{}
		ctx := logr.NewContext(context.Background(), funcr.New(func(p, a string) {
			logs.WriteString(p + " " + a + "\n")
		}, funcr.Options{Verbosity: 10}))
		name := "usage-live-" + strconv.FormatInt(time.Now().Unix(), 36)
		key := types.NamespacedName{Namespace: "default", Name: name}
		run := types.NamespacedName{Namespace: key.Namespace, Name: "kd-" + name}
		Expect(k8sClient.Create(ctx, &corev1alpha1.Agent{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: key.Namespace},
			Spec: corev1alpha1.AgentSpec{
				Image: "registry.k8s.io/pause:3.10", Command: []string{"/pause"}, Model: model,
				Budget: resource.MustParse("0.5"), DeadlineSeconds: 120,
			},
		})).To(Succeed())
		DeferCleanup(func() {
			_ = k8sClient.Delete(context.Background(), &corev1alpha1.Agent{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: key.Namespace}})
		})
		DeferCleanup(func() {
			// Block the key also when the spec fails before the revoke. This runs before the delete.
			a := &corev1alpha1.Agent{}
			if k8sClient.Get(context.Background(), key, a) == nil && a.Status.KeyHash != "" {
				Expect(gw.BlockKey(context.Background(), a.Status.KeyHash)).To(Succeed(), "the cleanup could not block the key")
			}
		})
		r := &AgentReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), APIReader: k8sClient,
			Gateway: gw, GatewayURL: gw.BaseURL, Grace: time.Minute}
		reconcileOnce := func() {
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())
		}
		reconcileOnce()

		By("making model calls with the run key")
		sec := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, run, sec)).To(Succeed())
		runKey := string(sec.Data[keyField])
		// Do not give the key to a matcher: a failure message shows the value.
		Expect(strings.HasPrefix(runKey, "sk-kd-")).To(BeTrue(), "the run Secret has no key")
		const calls = 2
		for i := range calls {
			// The gateway log keeps one row for each response id. The spike mock makes the id from
			// the task and the call in the message, so each call has a different id.
			msg := "Say OK. task=" + name + " call=" + strconv.Itoa(i) + " seed=" + strconv.Itoa(i+1)
			body := `{"model":"` + model + `","max_tokens":4,"messages":[{"role":"user","content":"` + msg + `"}]}`
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, gw.BaseURL+"/v1/chat/completions",
				strings.NewReader(body))
			Expect(err).NotTo(HaveOccurred())
			req.Header.Set("Authorization", "Bearer "+runKey)
			req.Header.Set("Content-Type", "application/json")
			resp, err := gw.HTTP.Do(req)
			Expect(err).NotTo(HaveOccurred())
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			// Do not show the body: an error body can hold the key.
			Expect(resp.StatusCode).To(Equal(http.StatusOK), "the model call failed")
		}

		// spendLogs reads the gateway log by the key hash. The key value is not in the URL.
		spendLogs := func() []liveSpendLog {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet,
				gw.BaseURL+"/spend/logs?api_key="+url.QueryEscape(gateway.HashKey(runKey)), nil)
			Expect(err).NotTo(HaveOccurred())
			req.Header.Set("Authorization", "Bearer "+gw.MasterKey)
			resp, err := gw.HTTP.Do(req)
			Expect(err).NotTo(HaveOccurred())
			defer func() { _ = resp.Body.Close() }()
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
			var rows []liveSpendLog
			Expect(json.NewDecoder(resp.Body).Decode(&rows)).To(Succeed())
			return rows
		}
		// The gateway writes the log in batches. Wait for all the calls before the revoke, so the
		// settle window starts after the writes.
		Eventually(func() int { return len(spendLogs()) }).WithTimeout(30*time.Second).
			WithPolling(time.Second).Should(Equal(calls), "the gateway log has one row for each call")

		By("ending the Job and revoking the key")
		endRunJob(ctx, run, batchv1.JobComplete)
		reconcileOnce()
		a := &corev1alpha1.Agent{}
		Expect(k8sClient.Get(ctx, key, a)).To(Succeed())
		Expect(a.Status.Phase).To(Equal(corev1alpha1.AgentSucceeded))
		Expect(a.Status.Usage).NotTo(BeNil())

		By("reading the gateway log by the key hash")
		rows := spendLogs()
		want := corev1alpha1.AgentUsage{}
		var spend float64
		for _, x := range rows {
			spend += x.Spend
			if x.Status != "success" {
				want.FailedCalls++
				continue
			}
			want.Calls++
			want.InputTokens += x.PromptTokens
			want.OutputTokens += x.CompletionTokens
		}
		Expect(want.Calls).To(BeEquivalentTo(calls), "the gateway log has one row for each call")
		got := *a.Status.Usage
		GinkgoWriter.Printf("status usage %+v, gateway log %+v, log spend %v\n", got, want, spend)
		Expect(got.Calls).To(Equal(want.Calls))
		Expect(got.FailedCalls).To(Equal(want.FailedCalls))
		Expect(got.InputTokens).To(Equal(want.InputTokens))
		Expect(got.OutputTokens).To(Equal(want.OutputTokens))
		gotSpend, err := strconv.ParseFloat(got.Spend, 64)
		Expect(err).NotTo(HaveOccurred())
		Expect(gotSpend).To(BeNumerically("~", spend, 1e-9))

		By("keeping the key value out of the log and the events")
		// Use booleans: a matcher with the key would show the key in the failure message.
		Expect(strings.Contains(logs.String(), runKey)).To(BeFalse(), "a key value is in the log")
		events := &corev1.EventList{}
		Expect(k8sClient.List(ctx, events, client.InNamespace(key.Namespace))).To(Succeed())
		for _, e := range events.Items {
			Expect(strings.Contains(e.Message, runKey)).To(BeFalse(), "a key value is in an event")
		}
	})
})
