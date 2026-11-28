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
	"encoding/json"
	"errors"
	"io"
	"maps"
	"regexp"
	"slices"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apiconversion "k8s.io/apimachinery/pkg/conversion"
	"sigs.k8s.io/controller-runtime/pkg/conversion"

	corev1beta1 "github.com/mohanraj00/koodam/api/v1beta1"
)

// v1beta1 is the hub version. Each v1alpha1 kind converts to it and from it. conversion-gen makes
// the field copies in zz_generated.conversion.go. This file has the hand code for the fields that
// are not the same in the two versions. conversion-gen finds a hand function by its name and uses
// it, so a conversion through the scheme and a conversion through ConvertTo give the same result.
//
//   - TestKey.spec.testID has the name spec.testRunID in v1beta1.
//   - The plugins of a harness (Agent and Workspace) are an enum with one value in v1alpha1 and a
//     list of preset names in v1beta1.

// ConversionDataAnnotation is on the v1alpha1 form of an Agent or a Workspace that has v1beta1 data
// that v1alpha1 cannot hold. The value is a JSON object with the full v1beta1 plugin list of each
// harness that lost a plugin, by harness name. The conversion to v1beta1 uses it to get the list
// back, then removes it. The name is reserved: the conversion replaces or removes each other value.
// The value never has a credential.
const ConversionDataAnnotation = "core.koodam.dev/conversion-data"

const (
	// maxConversionData is the maximum size of the annotation value in bytes. A Workspace has at
	// most 4 harnesses, each with at most 8 plugin names of 63 characters.
	maxConversionData = 4096
	// maxPlugins and maxPluginLength are the v1beta1 limits of HarnessSpec.Plugins.
	maxPlugins      = 8
	maxPluginLength = 63
)

// pluginName is the v1beta1 pattern of a plugin name.
var pluginName = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// conversionData is the value of ConversionDataAnnotation.
type conversionData struct {
	// Plugins has the full v1beta1 plugin list of a harness, by harness name.
	Plugins map[string][]corev1beta1.HarnessPlugin `json:"plugins"`
}

// knownPlugins returns the plugins that v1alpha1 can hold, in the same order.
func knownPlugins[P ~string](all []P) []HarnessPlugin {
	var known []HarnessPlugin
	for _, p := range all {
		if HarnessPlugin(p) == HarnessPluginNookku {
			known = append(known, HarnessPluginNookku)
		}
	}
	return known
}

// lowerPlugins changes the harnesses of a v1alpha1 object that came from v1beta1. Each harness
// keeps only the plugins that v1alpha1 can hold. If a harness lost a plugin, the annotation gets
// the full list. If no harness lost a plugin, the object has no annotation.
func lowerPlugins(meta *metav1.ObjectMeta, harnesses []*HarnessSpec) error {
	data := conversionData{Plugins: map[string][]corev1beta1.HarnessPlugin{}}
	for _, h := range harnesses {
		known := knownPlugins(h.Plugins)
		if len(known) != len(h.Plugins) {
			full := make([]corev1beta1.HarnessPlugin, len(h.Plugins))
			for i, p := range h.Plugins {
				full[i] = corev1beta1.HarnessPlugin(p)
			}
			data.Plugins[h.Name] = full
		}
		h.Plugins = known
	}
	_, stale := meta.Annotations[ConversionDataAnnotation]
	if len(data.Plugins) == 0 && !stale {
		return nil
	}
	// The generated code copies the map header, so this object shares the map with the source.
	meta.Annotations = maps.Clone(meta.Annotations)
	delete(meta.Annotations, ConversionDataAnnotation)
	if len(data.Plugins) == 0 {
		return nil
	}
	value, err := json.Marshal(data)
	if err != nil {
		return err
	}
	if meta.Annotations == nil {
		meta.Annotations = map[string]string{}
	}
	meta.Annotations[ConversionDataAnnotation] = string(value)
	return nil
}

// raisePlugins changes the harnesses of a v1beta1 object that came from v1alpha1. It removes the
// annotation. A harness gets its full list from the annotation only if the v1alpha1 list is equal
// to the part of the full list that v1alpha1 can hold. If a v1alpha1 client changed the list, the
// v1alpha1 list stays.
func raisePlugins(meta *metav1.ObjectMeta, harnesses []*corev1beta1.HarnessSpec) {
	value, ok := meta.Annotations[ConversionDataAnnotation]
	if !ok {
		return
	}
	// The generated code copies the map header, so this object shares the map with the source.
	meta.Annotations = maps.Clone(meta.Annotations)
	delete(meta.Annotations, ConversionDataAnnotation)
	data, ok := parseConversionData(value)
	if !ok {
		return
	}
	for _, h := range harnesses {
		full, ok := data.Plugins[h.Name]
		same := func(a HarnessPlugin, b corev1beta1.HarnessPlugin) bool { return string(a) == string(b) }
		if ok && slices.EqualFunc(knownPlugins(full), h.Plugins, same) {
			h.Plugins = full
		}
	}
}

// parseConversionData reads the annotation value. A user can write the annotation, and the API
// server does not check it against the v1beta1 schema. Thus this function applies the v1beta1
// limits of the plugin list. If one part of the value is not valid, it refuses all of the value.
func parseConversionData(value string) (conversionData, bool) {
	data := conversionData{}
	if len(value) > maxConversionData {
		return data, false
	}
	decoder := json.NewDecoder(strings.NewReader(value))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&data); err != nil {
		return data, false
	}
	// The value must be one JSON object and no more text.
	if err := decoder.Decode(&json.RawMessage{}); !errors.Is(err, io.EOF) {
		return data, false
	}
	for _, full := range data.Plugins {
		if len(full) == 0 || len(full) > maxPlugins {
			return data, false
		}
		for i, p := range full {
			if len(p) > maxPluginLength || !pluginName.MatchString(string(p)) || slices.Contains(full[:i], p) {
				return data, false
			}
		}
	}
	return data, true
}

// Convert_v1alpha1_Agent_To_v1beta1_Agent copies the Agent, then gets the full plugin list from the annotation.
func Convert_v1alpha1_Agent_To_v1beta1_Agent(in *Agent, out *corev1beta1.Agent, s apiconversion.Scope) error {
	if err := autoConvert_v1alpha1_Agent_To_v1beta1_Agent(in, out, s); err != nil {
		return err
	}
	var harnesses []*corev1beta1.HarnessSpec
	if out.Spec.Harness != nil {
		harnesses = append(harnesses, out.Spec.Harness)
	}
	raisePlugins(&out.ObjectMeta, harnesses)
	return nil
}

// Convert_v1beta1_Agent_To_v1alpha1_Agent copies the Agent, then keeps only the plugins that v1alpha1 can hold.
func Convert_v1beta1_Agent_To_v1alpha1_Agent(in *corev1beta1.Agent, out *Agent, s apiconversion.Scope) error {
	if err := autoConvert_v1beta1_Agent_To_v1alpha1_Agent(in, out, s); err != nil {
		return err
	}
	var harnesses []*HarnessSpec
	if out.Spec.Harness != nil {
		harnesses = append(harnesses, out.Spec.Harness)
	}
	return lowerPlugins(&out.ObjectMeta, harnesses)
}

// Convert_v1alpha1_Workspace_To_v1beta1_Workspace copies the Workspace, then gets the full plugin lists from the annotation.
func Convert_v1alpha1_Workspace_To_v1beta1_Workspace(in *Workspace, out *corev1beta1.Workspace, s apiconversion.Scope) error {
	if err := autoConvert_v1alpha1_Workspace_To_v1beta1_Workspace(in, out, s); err != nil {
		return err
	}
	harnesses := make([]*corev1beta1.HarnessSpec, len(out.Spec.Harnesses))
	for i := range out.Spec.Harnesses {
		harnesses[i] = &out.Spec.Harnesses[i]
	}
	raisePlugins(&out.ObjectMeta, harnesses)
	return nil
}

// Convert_v1beta1_Workspace_To_v1alpha1_Workspace copies the Workspace, then keeps only the plugins that v1alpha1 can hold.
func Convert_v1beta1_Workspace_To_v1alpha1_Workspace(in *corev1beta1.Workspace, out *Workspace, s apiconversion.Scope) error {
	if err := autoConvert_v1beta1_Workspace_To_v1alpha1_Workspace(in, out, s); err != nil {
		return err
	}
	harnesses := make([]*HarnessSpec, len(out.Spec.Harnesses))
	for i := range out.Spec.Harnesses {
		harnesses[i] = &out.Spec.Harnesses[i]
	}
	return lowerPlugins(&out.ObjectMeta, harnesses)
}

// Convert_v1alpha1_TestKeySpec_To_v1beta1_TestKeySpec copies the spec. testID has the name testRunID in v1beta1.
func Convert_v1alpha1_TestKeySpec_To_v1beta1_TestKeySpec(in *TestKeySpec, out *corev1beta1.TestKeySpec, s apiconversion.Scope) error {
	out.TestRunID = in.TestID
	return autoConvert_v1alpha1_TestKeySpec_To_v1beta1_TestKeySpec(in, out, s)
}

// Convert_v1beta1_TestKeySpec_To_v1alpha1_TestKeySpec copies the spec. testRunID has the name testID in v1alpha1.
func Convert_v1beta1_TestKeySpec_To_v1alpha1_TestKeySpec(in *corev1beta1.TestKeySpec, out *TestKeySpec, s apiconversion.Scope) error {
	out.TestID = in.TestRunID
	return autoConvert_v1beta1_TestKeySpec_To_v1alpha1_TestKeySpec(in, out, s)
}

// ConvertTo converts this App to the hub version.
func (src *App) ConvertTo(hub conversion.Hub) error {
	return Convert_v1alpha1_App_To_v1beta1_App(src, hub.(*corev1beta1.App), nil)
}

// ConvertFrom converts the hub version to this App.
func (dst *App) ConvertFrom(hub conversion.Hub) error {
	return Convert_v1beta1_App_To_v1alpha1_App(hub.(*corev1beta1.App), dst, nil)
}

// ConvertTo converts this AppVersion to the hub version.
func (src *AppVersion) ConvertTo(hub conversion.Hub) error {
	return Convert_v1alpha1_AppVersion_To_v1beta1_AppVersion(src, hub.(*corev1beta1.AppVersion), nil)
}

// ConvertFrom converts the hub version to this AppVersion.
func (dst *AppVersion) ConvertFrom(hub conversion.Hub) error {
	return Convert_v1beta1_AppVersion_To_v1alpha1_AppVersion(hub.(*corev1beta1.AppVersion), dst, nil)
}

// ConvertTo converts this ApprovalRecord to the hub version.
func (src *ApprovalRecord) ConvertTo(hub conversion.Hub) error {
	return Convert_v1alpha1_ApprovalRecord_To_v1beta1_ApprovalRecord(src, hub.(*corev1beta1.ApprovalRecord), nil)
}

// ConvertFrom converts the hub version to this ApprovalRecord.
func (dst *ApprovalRecord) ConvertFrom(hub conversion.Hub) error {
	return Convert_v1beta1_ApprovalRecord_To_v1alpha1_ApprovalRecord(hub.(*corev1beta1.ApprovalRecord), dst, nil)
}

// ConvertTo converts this Hold to the hub version.
func (src *Hold) ConvertTo(hub conversion.Hub) error {
	return Convert_v1alpha1_Hold_To_v1beta1_Hold(src, hub.(*corev1beta1.Hold), nil)
}

// ConvertFrom converts the hub version to this Hold.
func (dst *Hold) ConvertFrom(hub conversion.Hub) error {
	return Convert_v1beta1_Hold_To_v1alpha1_Hold(hub.(*corev1beta1.Hold), dst, nil)
}

// ConvertTo converts this Profile to the hub version.
func (src *Profile) ConvertTo(hub conversion.Hub) error {
	return Convert_v1alpha1_Profile_To_v1beta1_Profile(src, hub.(*corev1beta1.Profile), nil)
}

// ConvertFrom converts the hub version to this Profile.
func (dst *Profile) ConvertFrom(hub conversion.Hub) error {
	return Convert_v1beta1_Profile_To_v1alpha1_Profile(hub.(*corev1beta1.Profile), dst, nil)
}

// ConvertTo converts this SpendBudget to the hub version.
func (src *SpendBudget) ConvertTo(hub conversion.Hub) error {
	return Convert_v1alpha1_SpendBudget_To_v1beta1_SpendBudget(src, hub.(*corev1beta1.SpendBudget), nil)
}

// ConvertFrom converts the hub version to this SpendBudget.
func (dst *SpendBudget) ConvertFrom(hub conversion.Hub) error {
	return Convert_v1beta1_SpendBudget_To_v1alpha1_SpendBudget(hub.(*corev1beta1.SpendBudget), dst, nil)
}

// ConvertTo converts this TenantQuota to the hub version.
func (src *TenantQuota) ConvertTo(hub conversion.Hub) error {
	return Convert_v1alpha1_TenantQuota_To_v1beta1_TenantQuota(src, hub.(*corev1beta1.TenantQuota), nil)
}

// ConvertFrom converts the hub version to this TenantQuota.
func (dst *TenantQuota) ConvertFrom(hub conversion.Hub) error {
	return Convert_v1beta1_TenantQuota_To_v1alpha1_TenantQuota(hub.(*corev1beta1.TenantQuota), dst, nil)
}

// ConvertTo converts this TestKey to the hub version.
func (src *TestKey) ConvertTo(hub conversion.Hub) error {
	return Convert_v1alpha1_TestKey_To_v1beta1_TestKey(src, hub.(*corev1beta1.TestKey), nil)
}

// ConvertFrom converts the hub version to this TestKey.
func (dst *TestKey) ConvertFrom(hub conversion.Hub) error {
	return Convert_v1beta1_TestKey_To_v1alpha1_TestKey(hub.(*corev1beta1.TestKey), dst, nil)
}

// ConvertTo converts this Agent to the hub version.
func (src *Agent) ConvertTo(hub conversion.Hub) error {
	return Convert_v1alpha1_Agent_To_v1beta1_Agent(src, hub.(*corev1beta1.Agent), nil)
}

// ConvertFrom converts the hub version to this Agent.
func (dst *Agent) ConvertFrom(hub conversion.Hub) error {
	return Convert_v1beta1_Agent_To_v1alpha1_Agent(hub.(*corev1beta1.Agent), dst, nil)
}

// ConvertTo converts this Workspace to the hub version.
func (src *Workspace) ConvertTo(hub conversion.Hub) error {
	return Convert_v1alpha1_Workspace_To_v1beta1_Workspace(src, hub.(*corev1beta1.Workspace), nil)
}

// ConvertFrom converts the hub version to this Workspace.
func (dst *Workspace) ConvertFrom(hub conversion.Hub) error {
	return Convert_v1beta1_Workspace_To_v1alpha1_Workspace(hub.(*corev1beta1.Workspace), dst, nil)
}
