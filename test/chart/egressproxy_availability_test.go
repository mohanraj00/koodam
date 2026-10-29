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
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
)

// renderRelease renders the chart with proxyValues and values for one release name and namespace.
func renderRelease(t *testing.T, release, namespace, values string) string {
	t.Helper()
	helm, err := exec.LookPath("helm")
	if err != nil {
		if os.Getenv("CI") != "" {
			t.Fatal("helm is not on the PATH")
		}
		t.Skip("helm is not on the PATH")
	}
	dir := t.TempDir()
	files := []string{proxyValues, values}
	args := make([]string, 0, 7+2*len(files))
	args = append(args, "template", release, chartDir, "--namespace", namespace,
		"-f", filepath.Join(chartDir, "values-bundled.yaml"))
	for i, v := range files {
		file := filepath.Join(dir, fmt.Sprintf("values-%d.yaml", i))
		if err := os.WriteFile(file, []byte(v), 0o600); err != nil {
			t.Fatal(err)
		}
		args = append(args, "-f", file)
	}
	out, err := exec.Command(helm, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("helm template: %v\n%s", err, out)
	}
	return string(out)
}

// podSpec returns the pod spec and the selector of the Deployment with the given name.
func podSpec(t *testing.T, out, name string) (corev1.PodSpec, map[string]string) {
	t.Helper()
	deps := decode[appsv1.Deployment](t, out, "Deployment", name)
	if len(deps) != 1 {
		t.Fatalf("got %d proxy Deployments, want 1", len(deps))
	}
	return deps[0].Spec.Template.Spec, deps[0].Spec.Selector.MatchLabels
}

func TestEgressProxyAvailabilityDefaults(t *testing.T) {
	out := render(t, proxyValues)
	pod, sel := podSpec(t, out, proxyName)
	if pod.Affinity != nil {
		t.Fatalf("got the affinity %+v, want none", pod.Affinity)
	}
	tsc := pod.TopologySpreadConstraints
	if len(tsc) != 1 || tsc[0].MaxSkew != 1 || tsc[0].TopologyKey != "kubernetes.io/hostname" ||
		tsc[0].WhenUnsatisfiable != corev1.ScheduleAnyway {
		t.Fatalf("got the spread %+v, want 1 soft spread across the nodes", tsc)
	}
	if tsc[0].LabelSelector == nil || !maps.Equal(tsc[0].LabelSelector.MatchLabels, sel) {
		t.Fatalf("the spread selector %+v is not the Deployment selector %v", tsc[0].LabelSelector, sel)
	}
	pdbs := decode[policyv1.PodDisruptionBudget](t, out, "PodDisruptionBudget", proxyName)
	if len(pdbs) != 1 || pdbs[0].Namespace != releaseNS || pdbs[0].Spec.MaxUnavailable == nil ||
		pdbs[0].Spec.MaxUnavailable.IntValue() != 1 || pdbs[0].Spec.MinAvailable != nil {
		t.Fatalf("got the PodDisruptionBudgets %+v, want 1 with maxUnavailable 1", pdbs)
	}
	if s := pdbs[0].Spec.Selector; s == nil || !maps.Equal(s.MatchLabels, sel) || len(s.MatchExpressions) != 0 {
		t.Fatalf("the PodDisruptionBudget selector %+v is not the Deployment selector %v", s, sel)
	}
}

func TestEgressProxyAvailabilityOverrides(t *testing.T) {
	out := renderRelease(t, "koodam", releaseNS, `
egressProxy:
  affinity:
    nodeAffinity:
      requiredDuringSchedulingIgnoredDuringExecution:
        nodeSelectorTerms:
        - matchExpressions:
          - {key: pool, operator: In, values: [egress]}
  topologySpreadConstraints:
  - maxSkew: 2
    topologyKey: topology.kubernetes.io/zone
    whenUnsatisfiable: DoNotSchedule
    labelSelector:
      matchLabels: {team: a}
  podDisruptionBudget:
    maxUnavailable: 50%
`)
	pod, _ := podSpec(t, out, proxyName)
	if pod.Affinity == nil || pod.Affinity.NodeAffinity == nil ||
		pod.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution == nil {
		t.Fatalf("got the affinity %+v, want the node affinity of the values", pod.Affinity)
	}
	tsc := pod.TopologySpreadConstraints
	if len(tsc) != 1 || tsc[0].MaxSkew != 2 || tsc[0].TopologyKey != "topology.kubernetes.io/zone" ||
		tsc[0].WhenUnsatisfiable != corev1.DoNotSchedule ||
		!maps.Equal(tsc[0].LabelSelector.MatchLabels, map[string]string{"team": "a"}) {
		t.Fatalf("got the spread %+v, want the spread of the values with its own labelSelector", tsc)
	}
	pdbs := decode[policyv1.PodDisruptionBudget](t, out, "PodDisruptionBudget", proxyName)
	if len(pdbs) != 1 || pdbs[0].Spec.MaxUnavailable.String() != "50%" {
		t.Fatalf("got the PodDisruptionBudgets %+v, want maxUnavailable 50%%", pdbs)
	}

	out = renderRelease(t, "koodam", releaseNS, `
egressProxy:
  topologySpreadConstraints: []
  podDisruptionBudget: {enabled: false}
`)
	if pod, _ := podSpec(t, out, proxyName); len(pod.TopologySpreadConstraints) != 0 {
		t.Fatalf("got the spread %+v, want none for an empty list", pod.TopologySpreadConstraints)
	}
	if len(objects(t, out, "PodDisruptionBudget")) != 0 {
		t.Fatal("the chart renders a PodDisruptionBudget, but podDisruptionBudget.enabled is false")
	}
}

// TestEgressProxyAvailabilityReleases checks that the selectors of 2 releases in 2 namespaces
// select only the proxy pods of their release.
func TestEgressProxyAvailabilityReleases(t *testing.T) {
	type release struct{ name, namespace, object string }
	releases := []release{
		{"koodam", releaseNS, proxyName},
		{"team-b", "team-b", "team-b-koodam-egress-proxy"},
	}
	sels := make([]map[string]string, 0, len(releases))
	for _, r := range releases {
		out := renderRelease(t, r.name, r.namespace, "")
		pod, sel := podSpec(t, out, r.object)
		if sel["app.kubernetes.io/instance"] != r.name || sel["app.kubernetes.io/component"] != "egress-proxy" {
			t.Fatalf("release %s: the Deployment selector %v does not select its proxy pods only", r.name, sel)
		}
		if got := pod.TopologySpreadConstraints[0].LabelSelector.MatchLabels; !maps.Equal(got, sel) {
			t.Fatalf("release %s: the spread selector %v is not %v", r.name, got, sel)
		}
		pdbs := decode[policyv1.PodDisruptionBudget](t, out, "PodDisruptionBudget", r.object)
		if len(pdbs) != 1 || pdbs[0].Namespace != r.namespace || !maps.Equal(pdbs[0].Spec.Selector.MatchLabels, sel) {
			t.Fatalf("release %s: got the PodDisruptionBudgets %+v, want 1 in %s with the selector %v",
				r.name, pdbs, r.namespace, sel)
		}
		sels = append(sels, sel)
	}
	if maps.Equal(sels[0], sels[1]) {
		t.Fatalf("the 2 releases have the same proxy selector %v", sels[0])
	}
}

func TestEgressProxyAvailabilityOff(t *testing.T) {
	out := render(t, "egressProxy: {podDisruptionBudget: {enabled: true}}")
	if len(objects(t, out, "PodDisruptionBudget")) != 0 || strings.Contains(out, "egress-proxy") {
		t.Fatal("the chart renders a proxy object, but the proxy is off")
	}
	if pod, _ := podSpec(t, out, "koodam-controller-manager"); len(pod.TopologySpreadConstraints) != 0 {
		t.Fatal("the proxy spread applies to the controller")
	}
}

func TestEgressProxyAvailabilityRefusesNoMaxUnavailable(t *testing.T) {
	out, err := helmTemplate(t, proxyValues, "egressProxy.podDisruptionBudget.maxUnavailable=null")
	if err == nil || !strings.Contains(out, "maxUnavailable is necessary") {
		t.Fatalf("got no error about maxUnavailable:\n%s", out)
	}
}
