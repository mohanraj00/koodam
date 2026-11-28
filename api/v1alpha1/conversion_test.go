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

package v1alpha1

import (
	"fmt"
	"math/rand"
	"os"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/api/apitesting/fuzzer"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	metafuzzer "k8s.io/apimachinery/pkg/apis/meta/fuzzer"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apimachinery/pkg/util/diff"
	"sigs.k8s.io/controller-runtime/pkg/conversion"
	"sigs.k8s.io/randfill"

	corev1beta1 "github.com/mohanraj00/koodam/api/v1beta1"
)

// seeds is the count of random objects of each kind in a usual test run. The command
// go test -fuzz=FuzzAgent ./api/v1alpha1/ tries more seeds for one kind.
const seeds = 25

const (
	// hubNookku is the one v1alpha1 plugin value, as a v1beta1 name.
	hubNookku = corev1beta1.HarnessPlugin(HarnessPluginNookku)
	codex     = "codex"
	runID     = "run_1-a"
)

// fillFuncs keep the changed fields of a random object in the values that the API server permits.
// All the other fields are random.
func fillFuncs(serializer.CodecFactory) []any {
	names := []corev1beta1.HarnessPlugin{hubNookku, "a", "b-2", "c3", "d", "e", "f", "g"}
	return []any{
		// A v1alpha1 list has no value or the one value of the enum.
		func(h *HarnessSpec, c randfill.Continue) {
			c.FillNoCustom(h)
			h.Plugins = nil
			if c.Bool() {
				h.Plugins = []HarnessPlugin{HarnessPluginNookku}
			}
		},
		// A v1beta1 list is a set of at most 8 names, in a random sequence.
		func(h *corev1beta1.HarnessSpec, c randfill.Continue) {
			c.FillNoCustom(h)
			h.Plugins = nil
			for _, i := range c.Perm(len(names))[:c.Intn(len(names)+1)] {
				h.Plugins = append(h.Plugins, names[i])
			}
		},
		// Each harness of a Workspace has a different name.
		func(s *corev1beta1.WorkspaceSpec, c randfill.Continue) {
			c.FillNoCustom(s)
			for i := range s.Harnesses {
				s.Harnesses[i].Name = fmt.Sprintf("harness-%d", i)
			}
		},
	}
}

// roundTrip fills one random object of each version from the seed. It converts the object to the
// other version and back, and makes sure that the result is equal to the object. It also makes
// sure that a conversion does not change its source.
func roundTrip[S, H any, PS interface {
	*S
	conversion.Convertible
}, PH interface {
	*H
	conversion.Hub
}](t *testing.T, seed int64) {
	t.Helper()
	codecs := serializer.NewCodecFactory(runtime.NewScheme())
	filler := fuzzer.FuzzerFor(fuzzer.MergeFuzzerFuncs(metafuzzer.Funcs, fillFuncs), rand.NewSource(seed), codecs)
	same := func(step string, want, got runtime.Object) {
		t.Helper()
		if !apiequality.Semantic.DeepEqual(want, got) {
			t.Fatalf("seed %d: %s:\n%s", seed, step, diff.Diff(want, got))
		}
	}

	spoke, hub, back := PS(new(S)), PH(new(H)), PS(new(S))
	filler.Fill(spoke)
	source := spoke.DeepCopyObject()
	if err := spoke.ConvertTo(hub); err != nil {
		t.Fatal(err)
	}
	same("the conversion to v1beta1 changed its source", source, spoke)
	if err := back.ConvertFrom(hub); err != nil {
		t.Fatal(err)
	}
	same("v1alpha1 to v1beta1 to v1alpha1 changed the object", spoke, back)

	hub, spoke, hubBack := PH(new(H)), PS(new(S)), PH(new(H))
	filler.Fill(hub)
	source = hub.DeepCopyObject()
	if err := spoke.ConvertFrom(hub); err != nil {
		t.Fatal(err)
	}
	same("the conversion to v1alpha1 changed its source", source, hub)
	if err := spoke.ConvertTo(hubBack); err != nil {
		t.Fatal(err)
	}
	same("v1beta1 to v1alpha1 to v1beta1 changed the object", hub, hubBack)
}

// fuzzKind is the fuzz test of one kind. A usual test run uses the seeds 0 to seeds-1.
func fuzzKind[S, H any, PS interface {
	*S
	conversion.Convertible
}, PH interface {
	*H
	conversion.Hub
}](f *testing.F) {
	for seed := range int64(seeds) {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, seed int64) { roundTrip[S, H, PS, PH](t, seed) })
}

func FuzzAgent(f *testing.F)          { fuzzKind[Agent, corev1beta1.Agent](f) }
func FuzzApp(f *testing.F)            { fuzzKind[App, corev1beta1.App](f) }
func FuzzAppVersion(f *testing.F)     { fuzzKind[AppVersion, corev1beta1.AppVersion](f) }
func FuzzApprovalRecord(f *testing.F) { fuzzKind[ApprovalRecord, corev1beta1.ApprovalRecord](f) }
func FuzzHold(f *testing.F)           { fuzzKind[Hold, corev1beta1.Hold](f) }
func FuzzProfile(f *testing.F)        { fuzzKind[Profile, corev1beta1.Profile](f) }
func FuzzSpendBudget(f *testing.F)    { fuzzKind[SpendBudget, corev1beta1.SpendBudget](f) }
func FuzzTenantQuota(f *testing.F)    { fuzzKind[TenantQuota, corev1beta1.TenantQuota](f) }
func FuzzTestKey(f *testing.F)        { fuzzKind[TestKey, corev1beta1.TestKey](f) }
func FuzzWorkspace(f *testing.F)      { fuzzKind[Workspace, corev1beta1.Workspace](f) }

// TestEachKindConverts makes sure that the scheme has the conversion functions of each kind, and
// that each v1beta1 kind is a hub.
func TestEachKindConverts(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1beta1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	kinds := scheme.KnownTypes(SchemeGroupVersion)
	count := 0
	for name := range kinds {
		if strings.HasSuffix(name, "List") || strings.HasSuffix(name, "Options") || name == "WatchEvent" {
			continue
		}
		count++
		spoke, err := scheme.New(SchemeGroupVersion.WithKind(name))
		if err != nil {
			t.Fatal(err)
		}
		hub, err := scheme.New(corev1beta1.SchemeGroupVersion.WithKind(name))
		if err != nil {
			t.Fatalf("v1beta1 does not have the kind %s", name)
		}
		if _, ok := spoke.(conversion.Convertible); !ok {
			t.Errorf("the v1alpha1 %s does not convert to the hub", name)
		}
		if _, ok := hub.(conversion.Hub); !ok {
			t.Errorf("the v1beta1 %s is not a hub", name)
		}
		if err := scheme.Convert(spoke, hub, nil); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if count != 10 {
		t.Errorf("found %d kinds, want 10. Add a fuzz test for a new kind.", count)
	}
}

func TestTestKeyID(t *testing.T) {
	spoke, hub, back := &TestKey{Spec: TestKeySpec{TestID: runID}}, &corev1beta1.TestKey{}, &TestKey{}
	if err := spoke.ConvertTo(hub); err != nil || hub.Spec.TestRunID != runID {
		t.Fatalf("testRunID = %q, err = %v", hub.Spec.TestRunID, err)
	}
	if err := back.ConvertFrom(hub); err != nil || back.Spec.TestID != runID {
		t.Fatalf("testID = %q, err = %v", back.Spec.TestID, err)
	}
}

// agentHub returns a v1beta1 Agent with the plugins and the annotations.
func agentHub(annotations map[string]string, plugins ...corev1beta1.HarnessPlugin) *corev1beta1.Agent {
	return &corev1beta1.Agent{
		ObjectMeta: metav1.ObjectMeta{Annotations: annotations},
		Spec:       corev1beta1.AgentSpec{Harness: &corev1beta1.HarnessSpec{Name: codex, Plugins: plugins}},
	}
}

func TestPluginsThatV1alpha1CannotHold(t *testing.T) {
	hub := agentHub(map[string]string{"team": "a"}, "lint", hubNookku, "trace")
	spoke := &Agent{}
	if err := spoke.ConvertFrom(hub); err != nil {
		t.Fatal(err)
	}
	if got := spoke.Spec.Harness.Plugins; len(got) != 1 || got[0] != HarnessPluginNookku {
		t.Errorf("v1alpha1 plugins = %v, want only nookku", got)
	}
	want := `{"plugins":{"codex":["lint","nookku","trace"]}}`
	if got := spoke.Annotations[ConversionDataAnnotation]; got != want || spoke.Annotations["team"] != "a" {
		t.Errorf("annotations = %v, want the conversion data %s", spoke.Annotations, want)
	}
	if _, ok := hub.Annotations[ConversionDataAnnotation]; ok {
		t.Error("the conversion wrote the annotation to its source")
	}
	back := &corev1beta1.Agent{}
	if err := spoke.ConvertTo(back); err != nil {
		t.Fatal(err)
	}
	if !apiequality.Semantic.DeepEqual(hub, back) {
		t.Errorf("the round trip changed the object:\n%s", diff.Diff(hub, back))
	}

	// A conversion through the scheme gives the same result, also for the items of a list.
	scheme := runtime.NewScheme()
	if err := AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	spokes, hubs := &AgentList{}, &corev1beta1.AgentList{}
	if err := scheme.Convert(&corev1beta1.AgentList{Items: []corev1beta1.Agent{*hub}}, spokes, nil); err != nil {
		t.Fatal(err)
	}
	if !apiequality.Semantic.DeepEqual(spoke, &spokes.Items[0]) {
		t.Errorf("the scheme gives a different v1alpha1 Agent:\n%s", diff.Diff(spoke, &spokes.Items[0]))
	}
	if err := scheme.Convert(spokes, hubs, nil); err != nil {
		t.Fatal(err)
	}
	if !apiequality.Semantic.DeepEqual(hub, &hubs.Items[0]) {
		t.Errorf("the scheme gives a different v1beta1 Agent:\n%s", diff.Diff(hub, &hubs.Items[0]))
	}
}

// TestPluginLimitsAreTheV1beta1Markers makes sure that the limits for the annotation are the
// limits of the v1beta1 schema.
func TestPluginLimitsAreTheV1beta1Markers(t *testing.T) {
	source, err := os.ReadFile("../v1beta1/agent_types.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{
		fmt.Sprintf("// +kubebuilder:validation:MaxItems=%d\n\tPlugins []HarnessPlugin", maxPlugins),
		fmt.Sprintf("// +kubebuilder:validation:MaxLength=%d\n", maxPluginLength),
		"// +kubebuilder:validation:Pattern=`" + pluginName.String() + "`\ntype HarnessPlugin string",
	} {
		if !strings.Contains(string(source), marker) {
			t.Errorf("api/v1beta1/agent_types.go does not have the marker %q", marker)
		}
	}
}

func TestV1alpha1ChangeWins(t *testing.T) {
	data := `{"plugins":{"codex":["lint","nookku"]}}`
	for name, plugins := range map[string][]HarnessPlugin{"removed": nil, "added": {HarnessPluginNookku, HarnessPluginNookku}} {
		spoke := &Agent{
			ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{ConversionDataAnnotation: data}},
			Spec:       AgentSpec{Harness: &HarnessSpec{Name: codex, Plugins: plugins}},
		}
		hub := &corev1beta1.Agent{}
		if err := spoke.ConvertTo(hub); err != nil {
			t.Fatal(err)
		}
		if got := hub.Spec.Harness.Plugins; len(got) != len(plugins) {
			t.Errorf("%s: v1beta1 plugins = %v, want the v1alpha1 list %v", name, got, plugins)
		}
		if _, ok := hub.Annotations[ConversionDataAnnotation]; ok {
			t.Errorf("%s: the v1beta1 form has the annotation", name)
		}
	}
	// The data of a different harness name does not apply.
	spoke := &Agent{
		ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{ConversionDataAnnotation: data}},
		Spec:       AgentSpec{Harness: &HarnessSpec{Name: "claude-code", Plugins: []HarnessPlugin{HarnessPluginNookku}}},
	}
	hub := &corev1beta1.Agent{}
	if err := spoke.ConvertTo(hub); err != nil || len(hub.Spec.Harness.Plugins) != 1 {
		t.Errorf("plugins = %v, err = %v, want only nookku", hub.Spec.Harness.Plugins, err)
	}
}

func TestBadConversionDataIsDropped(t *testing.T) {
	long := strings.Repeat("a", maxPluginLength+1)
	bad := map[string]string{
		"not JSON":       `{"plugins":`,
		"unknown field":  `{"plugins":{"codex":["lint","nookku"]},"spec":{}}`,
		"second value":   `{"plugins":{"codex":["lint","nookku"]}} {}`,
		"more text":      `{"plugins":{"codex":["lint","nookku"]}}]`,
		"wrong type":     `{"plugins":["lint","nookku"]}`,
		"empty list":     `{"plugins":{"codex":[]}}`,
		"too many":       `{"plugins":{"codex":["nookku","a","b","c","d","e","f","g","h"]}}`,
		"bad characters": `{"plugins":{"codex":["../etc","nookku"]}}`,
		"upper case":     `{"plugins":{"codex":["Lint","nookku"]}}`,
		"empty name":     `{"plugins":{"codex":["","nookku"]}}`,
		"long name":      `{"plugins":{"codex":["` + long + `","nookku"]}}`,
		"duplicate":      `{"plugins":{"codex":["lint","lint","nookku"]}}`,
		"too large":      `{"plugins":{"codex":["lint","nookku"]}}` + strings.Repeat(" ", maxConversionData),
		"one bad list":   `{"plugins":{"codex":["lint","nookku"],"other":["BAD"]}}`,
	}
	for name, data := range bad {
		spoke := &Agent{
			ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{ConversionDataAnnotation: data}},
			Spec:       AgentSpec{Harness: &HarnessSpec{Name: codex, Plugins: []HarnessPlugin{HarnessPluginNookku}}},
		}
		hub := &corev1beta1.Agent{}
		if err := spoke.ConvertTo(hub); err != nil {
			t.Fatal(err)
		}
		if got := hub.Spec.Harness.Plugins; len(got) != 1 || got[0] != hubNookku {
			t.Errorf("%s: v1beta1 plugins = %v, want only nookku", name, got)
		}
		if _, ok := hub.Annotations[ConversionDataAnnotation]; ok {
			t.Errorf("%s: the v1beta1 form has the annotation", name)
		}
		if spoke.Annotations[ConversionDataAnnotation] != data {
			t.Errorf("%s: the conversion changed the annotations of its source", name)
		}
	}
}

func TestStaleConversionDataIsRemoved(t *testing.T) {
	stale := map[string]string{ConversionDataAnnotation: `{"plugins":{"codex":["lint"]}}`, "team": "a"}
	for name, hub := range map[string]*corev1beta1.Agent{
		"all plugins fit": agentHub(stale, hubNookku),
		"no harness":      {ObjectMeta: metav1.ObjectMeta{Annotations: stale}},
	} {
		spoke := &Agent{}
		if err := spoke.ConvertFrom(hub); err != nil {
			t.Fatal(err)
		}
		if _, ok := spoke.Annotations[ConversionDataAnnotation]; ok || spoke.Annotations["team"] != "a" {
			t.Errorf("%s: annotations = %v, want only team", name, spoke.Annotations)
		}
		if len(hub.Annotations) != 2 {
			t.Errorf("%s: the conversion changed the annotations of its source", name)
		}
	}
}

func TestWorkspacePluginsByHarnessName(t *testing.T) {
	hub := &corev1beta1.Workspace{Spec: corev1beta1.WorkspaceSpec{Harnesses: []corev1beta1.HarnessSpec{
		{Name: "claude-code", Plugins: []corev1beta1.HarnessPlugin{hubNookku}},
		{Name: codex, Plugins: []corev1beta1.HarnessPlugin{hubNookku, "trace"}},
	}}}
	spoke := &Workspace{}
	if err := spoke.ConvertFrom(hub); err != nil {
		t.Fatal(err)
	}
	want := `{"plugins":{"codex":["nookku","trace"]}}`
	if got := spoke.Annotations[ConversionDataAnnotation]; got != want {
		t.Fatalf("conversion data = %s, want %s", got, want)
	}
	// A v1alpha1 client changes the sequence of the harnesses. Each harness keeps its own list.
	spoke.Spec.Harnesses[0], spoke.Spec.Harnesses[1] = spoke.Spec.Harnesses[1], spoke.Spec.Harnesses[0]
	back := &corev1beta1.Workspace{}
	if err := spoke.ConvertTo(back); err != nil {
		t.Fatal(err)
	}
	if got := back.Spec.Harnesses; got[0].Name != codex || len(got[0].Plugins) != 2 || len(got[1].Plugins) != 1 {
		t.Errorf("harnesses = %v", got)
	}
}
