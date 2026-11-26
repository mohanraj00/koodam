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
	"strings"
	"testing"
)

const (
	guardKey     = "koodam_guard.py: |"
	guardSetting = "- koodam_guard.handler"
	guardEnv     = "name: KOODAM_GUARD_POLICY"
)

// litellmConfigMap gives the rendered ConfigMap of the bundled gateway.
func litellmConfigMap(t *testing.T, out string) string {
	t.Helper()
	for _, doc := range objects(t, out, "ConfigMap") {
		if strings.Contains(doc, "name: koodam-litellm\n") {
			return doc
		}
	}
	t.Fatal("the chart has no gateway ConfigMap")
	return ""
}

// With the guard off, the gateway configuration does not change. With the guard on, the gateway
// gets the hook, its registration and its policy.
func TestGuardValues(t *testing.T) {
	off, err := helmTemplate(t, "")
	if err != nil {
		t.Fatalf("helm template failed: %v\n%s", err, off)
	}
	for _, s := range []string{guardKey, guardSetting, guardEnv, "litellm_settings:"} {
		if strings.Contains(off, s) {
			t.Errorf("the install with no guard has %q", s)
		}
	}
	on, err := helmTemplate(t, "gateway: {bundled: {guard: {enabled: true}}}\n")
	if err != nil {
		t.Fatalf("helm template failed: %v\n%s", err, on)
	}
	for _, s := range []string{guardKey, "litellm_settings:\n      callbacks:\n", "      " + guardSetting + "\n",
		"async def async_pre_call_hook", guardEnv + "\n          value: " +
			`"{\"action\":\"record\",\"maxBytes\":65536,\"sources\":[\"toolResult\",\"document\"]}"`} {
		if !strings.Contains(on, s) {
			t.Errorf("the install with the guard does not have %q", s)
		}
	}
	if strings.Contains(on, "koodam_zone") {
		t.Error("the install with the guard and no zone has the zone hook")
	}
	if litellmConfigMap(t, off) == litellmConfigMap(t, on) {
		t.Error("the guard does not change the gateway ConfigMap")
	}
	block, err := helmTemplate(t, "gateway: {bundled: {guard: {enabled: true, action: block, sources: [document], "+
		"maxBytes: 1024}}}\n")
	if err != nil || !strings.Contains(block, `\"action\":\"block\",\"maxBytes\":1024,\"sources\":[\"document\"]`) {
		t.Errorf("the block policy is not in the gateway: %v", err)
	}
	if annotation(t, on) == annotation(t, block) {
		t.Error("a change of the guard policy does not start the gateway again")
	}
	// The --set flag gives an int64, not a float64.
	set, err := helmTemplate(t, "", "gateway.bundled.guard.enabled=true", "gateway.bundled.guard.maxBytes=1024")
	if err != nil || !strings.Contains(set, `\"maxBytes\":1024,`) {
		t.Errorf("a maxBytes value from --set is not in the gateway: %v\n%s", err, set)
	}
	for _, bad := range []string{"0", "-1", "1.5", "text"} {
		out, err := helmTemplate(t, "", "gateway.bundled.guard.enabled=true", "gateway.bundled.guard.maxBytes="+bad)
		if err == nil || !strings.Contains(out, "gateway.bundled.guard.maxBytes must be a positive integer.") {
			t.Errorf("the maxBytes value %s from --set is accepted: %v", bad, err)
		}
	}
}

// annotation gives the config checksum of the gateway pod.
func annotation(t *testing.T, out string) string {
	t.Helper()
	for _, doc := range objects(t, out, "Deployment") {
		if strings.Contains(doc, "name: koodam-litellm\n") {
			_, rest, _ := strings.Cut(doc, "checksum/config: ")
			line, _, _ := strings.Cut(rest, "\n")
			return line
		}
	}
	t.Fatal("the chart has no gateway Deployment")
	return ""
}

// The zone hook and the guard hook are both in the callbacks list, the zone hook first.
func TestGuardAndZoneHooks(t *testing.T) {
	const values = "zone: {name: zone-a}\ngateway:\n  bundled:\n    guard: {enabled: true}\n    litellm:\n" +
		"      modelList:\n      - {model_name: model-b, zone: zone-b, litellm_params: {model: openai/route-b}}\n"
	out, err := helmTemplate(t, values)
	if err != nil {
		t.Fatalf("helm template failed: %v\n%s", err, out)
	}
	cm := litellmConfigMap(t, out)
	zone, guard := strings.Index(cm, "- koodam_zone.handler"), strings.Index(cm, guardSetting)
	if zone < 0 || guard < 0 || zone > guard || strings.Count(cm, "callbacks:") != 1 {
		t.Errorf("the callbacks list does not have the zone hook and then the guard hook:\n%s", cm)
	}
	if !strings.Contains(cm, guardKey) || !strings.Contains(cm, "koodam_zone.py: |") {
		t.Error("the ConfigMap does not have both hook files")
	}
}

// The chart refuses a guard policy that is not valid, and a secret variable with the policy name.
func TestGuardRefusals(t *testing.T) {
	for name, values := range map[string]string{
		"action":   "gateway: {bundled: {guard: {enabled: true, action: drop}}}\n",
		"empty":    "gateway: {bundled: {guard: {enabled: true, sources: []}}}\n",
		"source":   "gateway: {bundled: {guard: {enabled: true, sources: [system]}}}\n",
		"string":   "gateway: {bundled: {guard: {enabled: true, sources: toolResult}}}\n",
		"zero":     "gateway: {bundled: {guard: {enabled: true, maxBytes: 0}}}\n",
		"fraction": "gateway: {bundled: {guard: {enabled: true, maxBytes: 1.5}}}\n",
		"text":     "gateway: {bundled: {guard: {enabled: true, maxBytes: \"1024\"}}}\n",
	} {
		if out, err := helmTemplate(t, values); err == nil || !strings.Contains(out, "gateway.bundled.guard.") {
			t.Errorf("%s: a guard policy that is not valid is accepted: %v", name, err)
		}
	}
	env := "gateway: {bundled: {litellm: {extraEnvFromSecrets: [{name: KOODAM_GUARD_POLICY, secretName: s, " +
		"secretKey: k}]}}}\n"
	if out, err := helmTemplate(t, env); err == nil || !strings.Contains(out, "The chart sets this variable") {
		t.Errorf("a secret variable with the policy name is accepted: %v", err)
	}
}
