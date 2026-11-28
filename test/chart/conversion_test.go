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
	"bytes"
	"compress/gzip"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	policyv1 "k8s.io/api/policy/v1"
	apix "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/yaml"
)

const (
	// conversionName is the name of each object of the conversion webhook in the release koodam.
	conversionName   = "koodam-conversion"
	conversionSecret = conversionName + "-tls"
	conversionHost   = conversionName + ".koodam-system.svc"
	injectAnnotation = "cert-manager.io/inject-ca-from"
	convertPort      = "convert"
	kindCRD          = "CustomResourceDefinition"
	kindDeployment   = "Deployment"
	kindSecret       = "Secret"
	kindPolicy       = "NetworkPolicy"
	kindBudget       = "PodDisruptionBudget"
	kindCertificate  = "Certificate"
	kindIssuer       = "Issuer"
)

// webhookCRDs are the CRDs whose kinds differ between v1alpha1 and v1beta1.
var webhookCRDs = []string{"agents.core.koodam.dev", "testkeys.core.koodam.dev", "workspaces.core.koodam.dev"}

func crds(t *testing.T, out string) []apix.CustomResourceDefinition {
	t.Helper()
	docs := objects(t, out, kindCRD)
	list := make([]apix.CustomResourceDefinition, 0, len(docs))
	for _, doc := range docs {
		var crd apix.CustomResourceDefinition
		if err := yaml.Unmarshal([]byte(doc), &crd); err != nil {
			t.Fatal(err)
		}
		list = append(list, crd)
	}
	return list
}

// twoVersions is true if the CRD has the deprecated v1alpha1 and the stored v1beta1.
func twoVersions(crd *apix.CustomResourceDefinition) bool {
	v := crd.Spec.Versions
	return len(v) == 2 && v[0].Name == "v1alpha1" && v[0].Served && !v[0].Storage && v[0].Deprecated &&
		v[1].Name == "v1beta1" && v[1].Served && v[1].Storage && !v[1].Deprecated
}

// webhookClient returns the client config of the conversion webhook of a CRD. It is nil if the
// CRD does not have the webhook of the release in the form that the API server needs.
func webhookClient(crd *apix.CustomResourceDefinition) *apix.WebhookClientConfig {
	c := crd.Spec.Conversion
	if c == nil || c.Strategy != apix.WebhookConverter || c.Webhook == nil || c.Webhook.ClientConfig == nil ||
		!slices.Equal(c.Webhook.ConversionReviewVersions, []string{"v1"}) {
		return nil
	}
	client := c.Webhook.ClientConfig
	svc := client.Service
	if svc == nil || client.URL != nil || svc.Name != conversionName || svc.Namespace != releaseNS ||
		svc.Path == nil || *svc.Path != "/convert" || svc.Port == nil || *svc.Port != 443 {
		return nil
	}
	return client
}

// checkCRDs checks the 7 core CRDs. Only the CRDs of webhookCRDs have the conversion webhook. It
// returns the CA bundle of the webhook, which must be the same in each one.
func checkCRDs(t *testing.T, out string) []byte {
	t.Helper()
	list := crds(t, out)
	if len(list) != 7 {
		t.Fatalf("got %d CRDs, want the 7 core CRDs", len(list))
	}
	var bundles [][]byte
	for _, crd := range list {
		if crd.Annotations["helm.sh/resource-policy"] != "keep" || crd.Labels["app.kubernetes.io/managed-by"] != "Helm" {
			t.Errorf("%s: want the keep policy and the chart labels", crd.Name)
		}
		if !twoVersions(&crd) {
			t.Errorf("%s: want the deprecated v1alpha1 and the stored v1beta1", crd.Name)
		}
		client := webhookClient(&crd)
		switch {
		case !slices.Contains(webhookCRDs, crd.Name):
			if crd.Spec.Conversion != nil || crd.Annotations[injectAnnotation] != "" {
				t.Errorf("%s: a kind with the same schema in the two versions must have no webhook", crd.Name)
			}
		case client == nil:
			t.Fatalf("%s: got the conversion %+v", crd.Name, crd.Spec.Conversion)
		default:
			bundles = append(bundles, client.CABundle)
		}
	}
	if len(bundles) != len(webhookCRDs) || !bytes.Equal(bundles[0], bundles[1]) || !bytes.Equal(bundles[0], bundles[2]) {
		t.Fatalf("want the same CA bundle in the %d CRDs with the webhook", len(webhookCRDs))
	}
	return bundles[0]
}

func one[T any](t *testing.T, out, kind string) T {
	t.Helper()
	got := decode[T](t, out, kind, conversionName)
	if len(got) != 1 {
		t.Fatalf("got %d %s objects with the name %s, want 1", len(got), kind, conversionName)
	}
	return got[0]
}

// webhookSecretName returns the name of the Secret that the webhook pod mounts.
func webhookSecretName(t *testing.T, out string) string {
	t.Helper()
	volumes := one[appsv1.Deployment](t, out, kindDeployment).Spec.Template.Spec.Volumes
	if len(volumes) != 1 || volumes[0].Secret == nil {
		t.Fatalf("got the volumes %+v, want only the Secret of the certificate", volumes)
	}
	return volumes[0].Secret.SecretName
}

// TestConversionCertificate makes sure that the chart makes the CA and the certificate, that the
// CRDs have the CA of the Secret, and that the certificate is valid for the Service name.
func TestConversionCertificate(t *testing.T) {
	out := render(t, "")
	bundle := checkCRDs(t, out)
	secrets := decode[corev1.Secret](t, out, kindSecret, conversionSecret)
	if len(secrets) != 1 || secrets[0].Type != corev1.SecretTypeTLS || !bytes.Equal(secrets[0].Data["ca.crt"], bundle) {
		t.Fatalf("want one TLS Secret %s with the CA of the CRDs", conversionSecret)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(bundle) {
		t.Fatal("the CA bundle has no certificate")
	}
	block, _ := pem.Decode(secrets[0].Data["tls.crt"])
	if block == nil {
		t.Fatal("tls.crt is not PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{conversionHost, conversionHost + ".cluster.local"} {
		if _, err := cert.Verify(x509.VerifyOptions{Roots: roots, DNSName: host}); err != nil {
			t.Errorf("the certificate is not valid for %s: %v", host, err)
		}
	}
	if block, _ := pem.Decode(secrets[0].Data["tls.key"]); block == nil {
		t.Error("tls.key is not PEM")
	}
	if webhookSecretName(t, out) != conversionSecret {
		t.Error("the webhook does not mount the Secret of the chart")
	}
}

// TestConversionPod makes sure that the webhook pod has no token, no environment and no volume
// other than the certificate.
func TestConversionPod(t *testing.T) {
	out := render(t, "")
	sa := one[corev1.ServiceAccount](t, out, "ServiceAccount")
	if sa.AutomountServiceAccountToken == nil || *sa.AutomountServiceAccountToken {
		t.Error("the ServiceAccount of the webhook mounts a token")
	}
	d := one[appsv1.Deployment](t, out, kindDeployment)
	pod := d.Spec.Template.Spec
	if d.Spec.Replicas == nil || *d.Spec.Replicas != 2 || d.Spec.Template.Annotations["checksum/tls"] == "" {
		t.Errorf("want 2 replicas and the checksum of the certificate, got %+v", d.Spec.Template.Annotations)
	}
	if pod.ServiceAccountName != conversionName || pod.AutomountServiceAccountToken == nil ||
		*pod.AutomountServiceAccountToken || len(pod.Containers) != 1 || len(pod.InitContainers) != 0 {
		t.Fatalf("got the pod %+v", pod)
	}
	c := pod.Containers[0]
	if !slices.Equal(c.Command, []string{"/conversion"}) || len(c.Args) != 0 || len(c.Env) != 0 || len(c.EnvFrom) != 0 {
		t.Errorf("want only the command /conversion, got %+v", c)
	}
	if len(c.Ports) != 1 || c.Ports[0].ContainerPort != 9443 || c.Ports[0].Name != convertPort {
		t.Errorf("got the ports %+v", c.Ports)
	}
	sc := c.SecurityContext
	if sc == nil || sc.ReadOnlyRootFilesystem == nil || !*sc.ReadOnlyRootFilesystem ||
		sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation ||
		!slices.Equal(sc.Capabilities.Drop, []corev1.Capability{"ALL"}) {
		t.Errorf("got the security context %+v", sc)
	}
	if len(c.VolumeMounts) != 1 || c.VolumeMounts[0].MountPath != "/etc/koodam/conversion" || !c.VolumeMounts[0].ReadOnly {
		t.Errorf("got the mounts %+v", c.VolumeMounts)
	}
	manager := decode[appsv1.Deployment](t, out, kindDeployment, "koodam-controller-manager")
	if len(manager) != 1 || manager[0].Spec.Template.Spec.Containers[0].Image != c.Image {
		t.Error("the webhook must use the image of the manager")
	}
}

func TestConversionNetwork(t *testing.T) {
	out := render(t, "")
	svc := one[corev1.Service](t, out, "Service")
	ports := svc.Spec.Ports
	if len(ports) != 1 || ports[0].Port != 443 || ports[0].TargetPort != intstr.FromString(convertPort) {
		t.Errorf("got the Service ports %+v", ports)
	}
	labels := one[appsv1.Deployment](t, out, kindDeployment).Spec.Template.Labels
	for k, v := range svc.Spec.Selector {
		if labels[k] != v {
			t.Errorf("the Service selector %s=%s does not select the webhook pods", k, v)
		}
	}
	if labels["control-plane"] != "" {
		t.Error("the webhook pods have the label of the manager pods")
	}
	pdb := one[policyv1.PodDisruptionBudget](t, out, kindBudget)
	if pdb.Spec.MinAvailable == nil || pdb.Spec.MinAvailable.IntValue() != 1 {
		t.Errorf("got the PodDisruptionBudget %+v", pdb.Spec)
	}
	np := one[networkingv1.NetworkPolicy](t, out, kindPolicy).Spec
	if len(np.PolicyTypes) != 2 || len(np.Egress) != 0 || len(np.Ingress) != 1 || len(np.Ingress[0].From) != 0 ||
		len(np.Ingress[0].Ports) != 1 || *np.Ingress[0].Ports[0].Port != intstr.FromInt32(9443) {
		t.Errorf("got the NetworkPolicy %+v", np)
	}
}

// TestConversionNetworkPeers makes sure that conversion.networkPolicy.from limits the sources of
// the webhook port, and that it adds no second rule.
func TestConversionNetworkPeers(t *testing.T) {
	out := render(t, `conversion:
  networkPolicy:
    from:
    - ipBlock:
        cidr: 10.0.0.0/28
    - namespaceSelector:
        matchLabels:
          kubernetes.io/metadata.name: kube-system`)
	np := one[networkingv1.NetworkPolicy](t, out, kindPolicy).Spec
	if len(np.Ingress) != 1 || len(np.Egress) != 0 || len(np.Ingress[0].Ports) != 1 ||
		*np.Ingress[0].Ports[0].Port != intstr.FromInt32(9443) {
		t.Fatalf("got the NetworkPolicy %+v", np)
	}
	from := np.Ingress[0].From
	if len(from) != 2 || from[0].IPBlock == nil || from[0].IPBlock.CIDR != "10.0.0.0/28" ||
		from[1].NamespaceSelector == nil ||
		from[1].NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"] != "kube-system" {
		t.Errorf("got the ingress peers %+v", from)
	}
}

func TestConversionOff(t *testing.T) {
	out := render(t, "conversion: {enabled: false}")
	if len(crds(t, out)) != 0 || strings.Contains(out, conversionName) {
		t.Error("the chart renders a core CRD or a webhook object, but conversion.enabled is false")
	}
	out = render(t, "conversion: {replicas: 1, networkPolicy: {enabled: false}}")
	if len(objects(t, out, kindBudget)) != 0 {
		t.Error("want no PodDisruptionBudget for 1 replica")
	}
	if len(decode[networkingv1.NetworkPolicy](t, out, kindPolicy, conversionName)) != 0 {
		t.Error("want no NetworkPolicy if it is off")
	}
}

func TestConversionExistingSecret(t *testing.T) {
	const ca, mine = "a CA certificate", "my-tls"
	bundle := base64.StdEncoding.EncodeToString([]byte(ca))
	given := "existingSecret: " + mine + ", caBundle: " + bundle
	out := render(t, "conversion: {tls: {"+given+"}}")
	if got := checkCRDs(t, out); string(got) != ca {
		t.Errorf("got the CA bundle %q", got)
	}
	made := len(decode[corev1.Secret](t, out, kindSecret, conversionSecret)) + len(objects(t, out, kindCertificate))
	if made != 0 {
		t.Error("the chart makes a certificate, but the values give a Secret")
	}
	if got := webhookSecretName(t, out); got != mine {
		t.Errorf("the webhook mounts the Secret %s", got)
	}
	for values, want := range map[string]string{
		"conversion: {tls: {existingSecret: " + mine + "}}":                "necessary together",
		"conversion: {tls: {caBundle: " + bundle + "}}":                    "necessary together",
		"conversion: {tls: {" + given + ", certManager: {enabled: true}}}": "Set only one",
		"conversion: {replicas: 0}":                                        "conversion.replicas must be 1 or more",
	} {
		if out, err := helmTemplate(t, values); err == nil || !strings.Contains(out, want) {
			t.Errorf("%s: got no error about %q:\n%.300s", values, want, out)
		}
	}
}

func TestConversionCertManager(t *testing.T) {
	out := render(t, "conversion: {tls: {certManager: {enabled: true}}}")
	if bundle := checkCRDs(t, out); len(bundle) != 0 {
		t.Error("the CRDs have a CA bundle, but cert-manager gives the CA")
	}
	for _, crd := range crds(t, out) {
		if slices.Contains(webhookCRDs, crd.Name) && crd.Annotations[injectAnnotation] != "koodam-system/"+conversionName {
			t.Errorf("%s: got the annotation %q", crd.Name, crd.Annotations[injectAnnotation])
		}
	}
	if len(decode[corev1.Secret](t, out, kindSecret, conversionSecret)) != 0 {
		t.Error("the chart makes the Secret, but cert-manager makes it")
	}
	cert := one[unstructured.Unstructured](t, out, kindCertificate)
	names, _, _ := unstructured.NestedStringSlice(cert.Object, "spec", "dnsNames")
	secret, _, _ := unstructured.NestedString(cert.Object, "spec", "secretName")
	issuer, _, _ := unstructured.NestedString(cert.Object, "spec", "issuerRef", "name")
	if !slices.Equal(names, []string{conversionHost, conversionHost + ".cluster.local"}) || secret != conversionSecret ||
		issuer != conversionName {
		t.Errorf("got the Certificate %v", cert.Object["spec"])
	}
	one[unstructured.Unstructured](t, out, kindIssuer)
	if got := webhookSecretName(t, out); got != conversionSecret {
		t.Errorf("the webhook mounts the Secret %s", got)
	}

	out = render(t, "conversion: {tls: {certManager: {enabled: true, issuerRef: {name: ca, kind: ClusterIssuer}}}}")
	cert = one[unstructured.Unstructured](t, out, kindCertificate)
	ref, _, _ := unstructured.NestedStringMap(cert.Object, "spec", "issuerRef")
	if ref["name"] != "ca" || ref["kind"] != "ClusterIssuer" {
		t.Errorf("got the issuer %v", ref)
	}
	if len(objects(t, out, kindIssuer)) != 0 {
		t.Error("the chart makes an Issuer, but the values name one")
	}
}

// releaseFile is a file of the chart in a Helm release. JSON has the data as base64.
type releaseFile struct {
	Name string `json:"name"`
	Data []byte `json:"data"`
}

// releaseJSON returns the JSON of a Helm release of the chart with the manifest. It has the parts
// of a release that have a size: the chart files, the values and the rendered objects. The test
// does not use "helm install --dry-run", because Helm 3 needs a cluster for it.
func releaseJSON(t *testing.T, manifest string) []byte {
	t.Helper()
	chart := map[string]any{}
	var templates, files []releaseFile
	err := filepath.WalkDir(chartDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		name, err := filepath.Rel(chartDir, path)
		if err != nil {
			return err
		}
		switch name = filepath.ToSlash(name); {
		case name == "Chart.yaml" || name == "values.yaml":
			// Helm keeps these 2 files as objects.
			var parsed any
			if err := yaml.Unmarshal(data, &parsed); err != nil {
				return err
			}
			chart[name] = parsed
		case strings.HasPrefix(name, "templates/"):
			templates = append(templates, releaseFile{Name: name, Data: data})
		default:
			files = append(files, releaseFile{Name: name, Data: data})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(templates) == 0 || len(files) == 0 || len(chart) != 2 {
		t.Fatalf("the chart has %d templates, %d files and %d of 2 objects", len(templates), len(files), len(chart))
	}
	chart["templates"], chart["files"] = templates, files
	release, err := json.Marshal(map[string]any{"name": "koodam", "namespace": releaseNS, "chart": chart,
		"manifest": manifest})
	if err != nil {
		t.Fatal(err)
	}
	return release
}

// TestReleaseSize makes sure that Helm can store the release. Helm keeps the chart and the
// rendered objects in one Secret, as JSON with gzip and base64, and the API server refuses a
// Secret with more than 1 MiB of data. The CRDs with the Kubernetes pod template are the largest
// part: the chart has each one, and the rendered objects have each one again.
func TestReleaseSize(t *testing.T) {
	const limit = 1 << 20
	// The test keeps 20% of the limit free for the values of an install and for the release data
	// that Helm adds.
	const budget = limit * 8 / 10
	// The install with each CRD is the largest one.
	manifest := render(t, "", "apps.enabled=true")
	for _, crd := range []string{webhookCRDs[0], "appversions.core.koodam.dev"} {
		if !strings.Contains(manifest, crd) {
			t.Fatalf("the rendered objects do not have the CRD %s", crd)
		}
	}
	release := releaseJSON(t, manifest)
	var packed bytes.Buffer
	zip, err := gzip.NewWriterLevel(&packed, gzip.BestCompression)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := zip.Write(release); err != nil {
		t.Fatal(err)
	}
	if err := zip.Close(); err != nil {
		t.Fatal(err)
	}
	size := base64.StdEncoding.EncodedLen(packed.Len())
	t.Logf("the release is %d bytes", size)
	if size > budget {
		t.Errorf("the release is %d bytes. The budget is %d bytes, and the limit of a Secret is %d bytes.",
			size, budget, limit)
	}
}
