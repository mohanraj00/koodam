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

package main

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/yaml"
)

const (
	ingressDir   = "../../config/agent-ingress"
	admissionDir = ingressDir + "/admission"
	guardFile    = admissionDir + "/guard.yaml"

	guardName   = "koodam-agent-ingress-admission"
	webhookName = "agent-ingress.admission.koodam.dev"
	objectName  = "koodam-agent-admission"
	servicePort = 443
	usesIngress = "(object != null && has(object.spec) && has(object.spec.ingressPort)) || " +
		"(oldObject != null && has(oldObject.spec) && has(oldObject.spec.ingressPort))"

	// The input markers. Each one is not valid, so an unrendered object fails closed.
	nsInput    = "KOODAM-INPUT-NAMESPACE"
	imageInput = "KOODAM-INPUT-IMAGE"
	caInput    = "KOODAM-INPUT-CA-BUNDLE"
)

func ref[T any](v T) *T { return &v }

// source returns the file and its YAML documents.
func source(t *testing.T, name string) ([]byte, [][]byte) {
	t.Helper()
	raw, err := os.ReadFile(name)
	if err != nil {
		t.Fatal("an admission source file did not read")
	}
	r := utilyaml.NewYAMLReader(bufio.NewReader(bytes.NewReader(raw)))
	var docs [][]byte
	for {
		d, err := r.Read()
		if errors.Is(err, io.EOF) {
			return raw, docs
		}
		if err != nil {
			t.Fatal("an admission source file is not valid YAML")
		}
		docs = append(docs, d)
	}
}

// strict decodes doc into obj and refuses each unknown or duplicate field.
func strict(t *testing.T, doc []byte, obj any) {
	t.Helper()
	if yaml.UnmarshalStrict(doc, obj) != nil {
		t.Fatal("an admission source document does not decode strictly")
	}
}

func TestInputMarkersAreNotValid(t *testing.T) {
	if len(validation.IsDNS1123Label(nsInput)) == 0 {
		t.Fatal("the namespace marker is a valid namespace")
	}
	// An image name with upper case and no registry is not a valid image reference.
	if strings.ToLower(imageInput) == imageInput || strings.ContainsAny(imageInput, "/.:@") {
		t.Fatal("the image marker can be a valid image reference")
	}
	if _, err := base64.StdEncoding.DecodeString(caInput); err == nil {
		t.Fatal("the CA marker is valid base64")
	}
}

func TestGuardSource(t *testing.T) {
	raw, docs := source(t, guardFile)
	if len(docs) != 1 || bytes.Count(raw, []byte(caInput)) != 1 || bytes.Count(raw, []byte(nsInput)) != 1 {
		t.Fatal("the guard is not one document with one CA marker and one namespace marker")
	}
	var g admissionregistrationv1.ValidatingWebhookConfiguration
	if yaml.Unmarshal(docs[0], &g) == nil {
		t.Fatal("the unrendered guard decoded with the CA marker")
	}
	// A fixed text that is not a certificate. Only this test replaces the marker.
	ca := []byte("koodam test value, not a certificate")
	strict(t, bytes.Replace(docs[0], []byte(caInput), []byte(base64.StdEncoding.EncodeToString(ca)), 1), &g)

	scope := admissionregistrationv1.NamespacedScope
	want := admissionregistrationv1.ValidatingWebhookConfiguration{
		TypeMeta:   metav1.TypeMeta{APIVersion: "admissionregistration.k8s.io/v1", Kind: "ValidatingWebhookConfiguration"},
		ObjectMeta: metav1.ObjectMeta{Name: guardName},
		Webhooks: []admissionregistrationv1.ValidatingWebhook{{
			Name: webhookName,
			ClientConfig: admissionregistrationv1.WebhookClientConfig{
				Service: &admissionregistrationv1.ServiceReference{
					Namespace: nsInput, Name: objectName, Path: ref(webhookPath), Port: ref[int32](servicePort),
				},
				CABundle: ca,
			},
			Rules: []admissionregistrationv1.RuleWithOperations{{
				Operations: []admissionregistrationv1.OperationType{admissionregistrationv1.Create, admissionregistrationv1.Update},
				Rule: admissionregistrationv1.Rule{
					APIGroups: []string{"core.koodam.dev"}, APIVersions: []string{"v1alpha1"},
					Resources: []string{"agents"}, Scope: &scope,
				},
			}},
			FailurePolicy:           ref(admissionregistrationv1.Fail),
			MatchPolicy:             ref(admissionregistrationv1.Equivalent),
			SideEffects:             ref(admissionregistrationv1.SideEffectClassNone),
			TimeoutSeconds:          ref[int32](5),
			AdmissionReviewVersions: []string{"v1"},
			MatchConditions:         []admissionregistrationv1.MatchCondition{{Name: "uses-ingress", Expression: usesIngress}},
		}},
	}
	if !equality.Semantic.DeepEqual(g, want) {
		t.Fatal("the guard is not the approved guard")
	}
	// The API server sets each absent selector to match all. The source must not set one.
	if wh := g.Webhooks[0]; wh.NamespaceSelector != nil || wh.ObjectSelector != nil {
		t.Fatal("the guard has a selector")
	}
}

// TestAdmissionSourceIsDormant makes sure that the admission directory has only the file guard.yaml in this
// step, thus no workload, Secret, RBAC object or Kustomization, and that no Kustomization makes an apply path.
func TestAdmissionSourceIsDormant(t *testing.T) {
	ents, err := os.ReadDir(admissionDir)
	if err != nil {
		t.Fatal("the admission directory did not read")
	}
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	if !slices.Equal(names, []string{"guard.yaml"}) {
		t.Fatal("the admission directory has a file other than guard.yaml")
	}
	err = filepath.WalkDir(ingressDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if slices.Contains([]string{"kustomization.yaml", "kustomization.yml", "Kustomization"}, d.Name()) {
			t.Error("the ingress directory has a Kustomization")
		}
		return nil
	})
	if err != nil {
		t.Fatal("the ingress directory did not read")
	}
}
