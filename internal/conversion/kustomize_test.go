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

package conversion

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// `make install` builds config/crd only. `make deploy` builds config/default, which has the
// conversion Service and its certificate. Thus only config/default can give a CRD the webhook.
const (
	crdPackage      = "../../config/crd"
	defaultPackage  = "../../config/default"
	kustomization   = "kustomization.yaml"
	conversionPatch = "crd_conversion_patch.yaml"
	crdKind         = "CustomResourceDefinition"
	keyKind         = "kind"
	keyMetadata     = "metadata"
	keyNamespace    = "namespace"
	keyConversion   = "conversion"
	caAnnotation    = "cert-manager.io/inject-ca-from"
)

// webhookCRDs are the CRDs of the kinds that differ between v1alpha1 and v1beta1.
var webhookCRDs = []string{"agents.core.koodam.dev", "testkeys.core.koodam.dev", "workspaces.core.koodam.dev"}

// The path of the conversion Service in a CRD.
var servicePath = []string{specKey, keyConversion, "webhook", "clientConfig", "service"}

// documents returns the objects of a YAML stream.
func documents(t *testing.T, data []byte) []object {
	t.Helper()
	var out []object
	for doc := range bytes.SplitSeq(data, []byte("\n---")) {
		o := object{}
		if err := yaml.Unmarshal(doc, &o); err != nil {
			t.Fatal(err)
		}
		if len(o) != 0 {
			out = append(out, o)
		}
	}
	return out
}

func readDocuments(t *testing.T, path ...string) []object {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(path...))
	if err != nil {
		t.Fatal(err)
	}
	return documents(t, data)
}

func readDocument(t *testing.T, path ...string) object {
	t.Helper()
	docs := readDocuments(t, path...)
	if len(docs) != 1 {
		t.Fatalf("%s: want 1 object, got %d", filepath.Join(path...), len(docs))
	}
	return docs[0]
}

func text(o object, path ...string) string {
	s, _ := at(o, path...).(string)
	return s
}

func list(o object, path ...string) []object {
	items, _ := at(o, path...).([]any)
	out := make([]object, 0, len(items))
	for _, item := range items {
		if m, ok := item.(object); ok {
			out = append(out, m)
		}
	}
	return out
}

// find returns the object of a kind with a name and a namespace, or nil.
func find(objects []object, kind, namespace, name string) object {
	for _, o := range objects {
		if text(o, keyKind) == kind && text(o, keyMetadata, keyName) == name &&
			text(o, keyMetadata, keyNamespace) == namespace {
			return o
		}
	}
	return nil
}

// installCRDs returns the CRDs that config/crd lists. Each one must be a file of controller-gen.
func installCRDs(t *testing.T) []object {
	t.Helper()
	resources, _ := readDocument(t, crdPackage, kustomization)["resources"].([]any)
	if len(resources) == 0 {
		t.Fatal("config/crd has no resource")
	}
	out := make([]object, 0, len(resources))
	for _, r := range resources {
		name, _ := r.(string)
		if !strings.HasPrefix(name, "bases/") {
			t.Fatalf("config/crd has the resource %q, which is not a file of bases/", name)
		}
		out = append(out, readDocument(t, crdPackage, name))
	}
	return out
}

// TestInstallPackageHasNoWebhook makes sure that `make install` cannot apply a CRD that names a
// webhook Service. That Service and its CA are not in config/crd.
func TestInstallPackageHasNoWebhook(t *testing.T) {
	k := readDocument(t, crdPackage, kustomization)
	for _, key := range []string{"patches", "patchesStrategicMerge", "patchesJson6902", "replacements",
		"components", "vars"} {
		if k[key] != nil {
			t.Errorf("config/crd has %q. Put a change of a CRD in config/default.", key)
		}
	}
	for _, crd := range installCRDs(t) {
		if at(crd, specKey, keyConversion) != nil {
			t.Errorf("%s has a conversion stanza in config/crd", text(crd, keyMetadata, keyName))
		}
	}
}

// TestDeployPackageGivesTheWebhookToThreeCRDs checks the patch and the CA annotation of
// config/default. The two must apply to the same 3 CRDs, and the Service must be there.
func TestDeployPackageGivesTheWebhookToThreeCRDs(t *testing.T) {
	k := readDocument(t, defaultPackage, kustomization)
	pattern := ""
	for _, p := range list(k, "patches") {
		if text(p, "path") == conversionPatch && text(p, "target", keyKind) == crdKind {
			pattern = text(p, "target", keyName)
		}
	}
	if pattern == "" {
		t.Fatalf("config/default has no patch %s with a CustomResourceDefinition target", conversionPatch)
	}
	// kustomize matches the full name.
	full := regexp.MustCompile("^(?:" + pattern + ")$")
	var patched []string
	for _, crd := range installCRDs(t) {
		if name := text(crd, keyMetadata, keyName); full.MatchString(name) {
			patched = append(patched, name)
		}
	}
	slices.Sort(patched)
	if !reflect.DeepEqual(patched, webhookCRDs) {
		t.Errorf("the patch applies to %v, want %v", patched, webhookCRDs)
	}

	service := readDocument(t, defaultPackage, conversionPatch)
	name := text(service, append(servicePath, keyName)...)
	if find(readDocuments(t, "../../config/manager/conversion.yaml"), "Service", "system", name) == nil ||
		text(service, append(servicePath, keyNamespace)...) != "system" ||
		text(service, specKey, keyConversion, "strategy") != "Webhook" {
		t.Errorf("the patch does not name the conversion Service of config/manager: %v", at(service, specKey))
	}

	replacements := 0
	for _, r := range list(k, "replacements") {
		var annotated []string
		for _, target := range list(r, "targets") {
			if text(target, "select", keyKind) == crdKind {
				annotated = append(annotated, text(target, "select", keyName))
			}
		}
		if len(annotated) == 0 {
			continue
		}
		replacements++
		slices.Sort(annotated)
		if !reflect.DeepEqual(annotated, webhookCRDs) {
			t.Errorf("a replacement puts the CA annotation on %v, want %v", annotated, webhookCRDs)
		}
	}
	if replacements != 2 {
		t.Errorf("want 2 replacements for the CA annotation (namespace and name), got %d", replacements)
	}
}

// build runs kustomize on a package. The Makefile installs kustomize in bin/. If it is not there
// and KUSTOMIZE is not set, the test does not run.
func build(t *testing.T, dir string) []object {
	t.Helper()
	bin := os.Getenv("KUSTOMIZE")
	if bin == "" {
		bin = "../../bin/kustomize"
	}
	if _, err := os.Stat(bin); err != nil {
		t.Skipf("no kustomize at %s: run `make kustomize`", bin)
	}
	out, err := exec.Command(bin, "build", dir).Output()
	if err != nil {
		t.Fatalf("kustomize build %s: %v", dir, err)
	}
	return documents(t, out)
}

// TestKustomizeOutput builds the two packages. The output of config/crd is what `make install`
// applies, and the output of config/default is what `make deploy` applies.
func TestKustomizeOutput(t *testing.T) {
	crds := 0
	for _, o := range build(t, crdPackage) {
		crds++
		if text(o, keyKind) != crdKind || at(o, specKey, keyConversion) != nil ||
			at(o, keyMetadata, "annotations", caAnnotation) != nil {
			t.Errorf("config/crd: %s %s is not a CRD with no webhook", text(o, keyKind), text(o, keyMetadata, keyName))
		}
	}
	if crds != len(installCRDs(t)) {
		t.Errorf("config/crd: got %d objects, want %d", crds, len(installCRDs(t)))
	}

	out := build(t, defaultPackage)
	var webhooks []string
	for _, o := range out {
		if text(o, keyKind) != crdKind {
			continue
		}
		name := text(o, keyMetadata, keyName)
		ca := text(o, keyMetadata, "annotations", caAnnotation)
		if text(o, specKey, keyConversion, "strategy") != "Webhook" {
			if at(o, specKey, keyConversion) != nil || ca != "" {
				t.Errorf("config/default: %s has a conversion stanza or a CA annotation and no webhook", name)
			}
			continue
		}
		webhooks = append(webhooks, name)
		namespace := text(o, append(servicePath, keyNamespace)...)
		if find(out, "Service", namespace, text(o, append(servicePath, keyName)...)) == nil {
			t.Errorf("config/default: %s names a Service that is not in the output: %v", name, at(o, servicePath...))
		}
		certNamespace, certName, _ := strings.Cut(ca, "/")
		if certNamespace != namespace || find(out, "Certificate", certNamespace, certName) == nil {
			t.Errorf("config/default: %s has the CA annotation %q, which names no Certificate of the output", name, ca)
		}
	}
	slices.Sort(webhooks)
	if !reflect.DeepEqual(webhooks, webhookCRDs) {
		t.Errorf("config/default: the CRDs with a webhook are %v, want %v", webhooks, webhookCRDs)
	}
}
