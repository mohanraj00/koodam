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
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/validation"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/yaml"
)

const (
	ingressDir   = "../../config/agent-ingress"
	admissionDir = ingressDir + "/admission"
	guardFile    = admissionDir + "/guard.yaml"
	workloadFile = admissionDir + "/workload.yaml"
	managerFile  = "../../config/manager/manager.yaml"

	guardName   = "koodam-agent-ingress-admission"
	webhookName = "agent-ingress.admission.koodam.dev"
	objectName  = "koodam-agent-admission"
	tlsSecret   = "koodam-agent-admission-tls"
	servicePort = 443
	nonroot     = 65532
	tlsMode     = 0o440
	usesIngress = "(object != null && has(object.spec) && has(object.spec.ingressPort)) || " +
		"(oldObject != null && has(oldObject.spec) && has(oldObject.spec.ingressPort))"

	// The input markers. Each one is not valid, so an unrendered object fails closed.
	nsInput    = "KOODAM-INPUT-NAMESPACE"
	imageInput = "KOODAM-INPUT-IMAGE"
	caInput    = "KOODAM-INPUT-CA-BUNDLE"
)

func podLabels() map[string]string {
	return map[string]string{"app.kubernetes.io/name": "koodam", "app.kubernetes.io/component": "agent-admission"}
}

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
					Namespace: nsInput, Name: objectName, Path: new(webhookPath), Port: new(int32(servicePort)),
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
			FailurePolicy:           new(admissionregistrationv1.Fail),
			MatchPolicy:             new(admissionregistrationv1.Equivalent),
			SideEffects:             new(admissionregistrationv1.SideEffectClassNone),
			TimeoutSeconds:          new(int32(5)),
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

func objectMeta() metav1.ObjectMeta {
	return metav1.ObjectMeta{Name: objectName, Namespace: nsInput, Labels: podLabels()}
}

func wantPod() corev1.PodSpec {
	mode := new(int32(tlsMode))
	tcp := &corev1.Probe{ProbeHandler: corev1.ProbeHandler{TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt32(servePort)}}}
	return corev1.PodSpec{
		ServiceAccountName:            objectName,
		AutomountServiceAccountToken:  new(false),
		EnableServiceLinks:            new(false),
		TerminationGracePeriodSeconds: new(int64(10)),
		SecurityContext: &corev1.PodSecurityContext{
			RunAsNonRoot: new(true), RunAsUser: new(int64(nonroot)), RunAsGroup: new(int64(nonroot)), FSGroup: new(int64(nonroot)),
			SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
		},
		TopologySpreadConstraints: []corev1.TopologySpreadConstraint{{
			MaxSkew: 1, TopologyKey: corev1.LabelHostname, WhenUnsatisfiable: corev1.ScheduleAnyway,
			LabelSelector: &metav1.LabelSelector{MatchLabels: podLabels()},
		}},
		Containers: []corev1.Container{{
			Name: "agent-admission", Image: imageInput, Command: []string{"/agent-admission"},
			Ports: []corev1.ContainerPort{{Name: "webhook", ContainerPort: servePort, Protocol: corev1.ProtocolTCP}},
			SecurityContext: &corev1.SecurityContext{
				ReadOnlyRootFilesystem: new(true), AllowPrivilegeEscalation: new(false),
				Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
			},
			LivenessProbe: tcp, ReadinessProbe: tcp,
			Resources: corev1.ResourceRequirements{
				Limits:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m"), corev1.ResourceMemory: resource.MustParse("128Mi")},
				Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("10m"), corev1.ResourceMemory: resource.MustParse("64Mi")},
			},
			VolumeMounts: []corev1.VolumeMount{{Name: "tls", MountPath: path.Dir(certFile), ReadOnly: true}},
		}},
		Volumes: []corev1.Volume{{Name: "tls", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
			SecretName: tlsSecret, DefaultMode: mode,
			Items: []corev1.KeyToPath{
				{Key: corev1.TLSCertKey, Path: path.Base(certFile), Mode: mode},
				{Key: corev1.TLSPrivateKeyKey, Path: path.Base(keyFile), Mode: mode},
			},
		}}}},
	}
}

func TestWorkloadSource(t *testing.T) {
	raw, docs := source(t, workloadFile)
	if len(docs) != 5 || bytes.Count(raw, []byte(nsInput)) != 5 || bytes.Count(raw, []byte(imageInput)) != 1 ||
		bytes.Contains(raw, []byte(caInput)) {
		t.Fatal("the workload does not have five documents and the exact input markers")
	}
	var (
		sa  corev1.ServiceAccount
		svc corev1.Service
		dep appsv1.Deployment
		pdb policyv1.PodDisruptionBudget
		np  networkingv1.NetworkPolicy
	)
	for i, obj := range []any{&sa, &svc, &dep, &pdb, &np} {
		strict(t, docs[i], obj)
	}
	selector := &metav1.LabelSelector{MatchLabels: podLabels()}
	checks := []struct {
		kind      string
		got, want any
	}{
		{"ServiceAccount", sa, corev1.ServiceAccount{
			TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "ServiceAccount"}, ObjectMeta: objectMeta(),
			AutomountServiceAccountToken: new(false),
		}},
		{"Service", svc, corev1.Service{
			TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Service"}, ObjectMeta: objectMeta(),
			Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, Selector: podLabels(), Ports: []corev1.ServicePort{{
				Name: "webhook", Protocol: corev1.ProtocolTCP, Port: servicePort, TargetPort: intstr.FromInt32(servePort),
			}}},
		}},
		{"Deployment", dep, appsv1.Deployment{
			TypeMeta: metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"}, ObjectMeta: objectMeta(),
			Spec: appsv1.DeploymentSpec{Replicas: new(int32(2)), Selector: selector, Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: podLabels()}, Spec: wantPod(),
			}},
		}},
		{"PodDisruptionBudget", pdb, policyv1.PodDisruptionBudget{
			TypeMeta: metav1.TypeMeta{APIVersion: "policy/v1", Kind: "PodDisruptionBudget"}, ObjectMeta: objectMeta(),
			Spec: policyv1.PodDisruptionBudgetSpec{MinAvailable: new(intstr.FromInt32(1)), Selector: selector},
		}},
		{"NetworkPolicy", np, networkingv1.NetworkPolicy{
			TypeMeta: metav1.TypeMeta{APIVersion: "networking.k8s.io/v1", Kind: "NetworkPolicy"}, ObjectMeta: objectMeta(),
			Spec: networkingv1.NetworkPolicySpec{
				PodSelector: *selector,
				PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress},
			},
		}},
	}
	for _, c := range checks {
		if !equality.Semantic.DeepEqual(c.got, c.want) {
			t.Fatalf("the %s is not the approved value", c.kind)
		}
	}

	// Semantic equality treats an empty list as equal to no list. Thus check the deny-all rules
	// and the absence of args, env and extra containers again with no such rule.
	pod := dep.Spec.Template.Spec
	if len(np.Spec.Ingress) != 0 || len(np.Spec.Egress) != 0 || !slices.Contains(np.Spec.PolicyTypes, networkingv1.PolicyTypeIngress) {
		t.Error("the NetworkPolicy permits traffic")
	}
	if len(pod.Containers) != 1 || len(pod.InitContainers) != 0 || len(pod.EphemeralContainers) != 0 ||
		len(pod.Containers[0].Args) != 0 || len(pod.Containers[0].Env) != 0 || len(pod.Containers[0].EnvFrom) != 0 {
		t.Error("the pod has another container, an argument or an environment variable")
	}
	for _, v := range pod.Volumes {
		if v.Projected != nil || v.Secret == nil || v.Secret.Optional != nil {
			t.Error("the pod has a volume other than the required TLS Secret")
		}
	}
	m := pod.Containers[0].VolumeMounts[0]
	items := pod.Volumes[0].Secret.Items
	if m.SubPath != "" || m.SubPathExpr != "" || path.Join(m.MountPath, items[0].Path) != certFile ||
		path.Join(m.MountPath, items[1].Path) != keyFile {
		t.Error("the TLS files are not at the paths of the binary")
	}
}

// TestSelectorDoesNotSelectTheManager makes sure that the admission Service, PDB and policy do not
// select the manager pod.
func TestSelectorDoesNotSelectTheManager(t *testing.T) {
	_, docs := source(t, managerFile)
	sel, err := metav1.LabelSelectorAsSelector(&metav1.LabelSelector{MatchLabels: podLabels()})
	if err != nil || sel.Empty() {
		t.Fatal("the admission selector is not valid")
	}
	found := false
	for _, d := range docs {
		var dep appsv1.Deployment
		if yaml.Unmarshal(d, &dep) != nil {
			t.Fatal("the manager source does not decode")
		}
		if dep.Kind == "Deployment" {
			found = true
			if sel.Matches(labels.Set(dep.Spec.Template.Labels)) {
				t.Fatal("the admission selector selects the manager pod")
			}
		}
	}
	if !found {
		t.Fatal("the manager source has no Deployment")
	}
}

// TestAdmissionSourceIsDormant makes sure that the admission directory has only the files guard.yaml and
// workload.yaml in this step, thus no Secret, RBAC object or Kustomization, and that no Kustomization makes
// an apply path.
func TestAdmissionSourceIsDormant(t *testing.T) {
	ents, err := os.ReadDir(admissionDir)
	if err != nil {
		t.Fatal("the admission directory did not read")
	}
	names := make([]string, 0, len(ents))
	for _, e := range ents {
		names = append(names, e.Name())
	}
	if !slices.Equal(names, []string{"guard.yaml", "workload.yaml"}) {
		t.Fatal("the admission directory has a file other than guard.yaml and workload.yaml")
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
