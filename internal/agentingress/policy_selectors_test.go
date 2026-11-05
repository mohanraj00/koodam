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

package agentingress

import (
	"testing"

	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const joined = "a,b"

type req = metav1.LabelSelectorRequirement

func sel(labels map[string]string, exprs ...req) *metav1.LabelSelector {
	return &metav1.LabelSelector{MatchLabels: labels, MatchExpressions: exprs}
}

func exists(key string) req { return req{Key: key, Operator: metav1.LabelSelectorOpExists} }

func TestSameSelector(t *testing.T) {
	in := func(key string, vs ...string) req {
		return req{Key: key, Operator: metav1.LabelSelectorOpIn, Values: vs}
	}
	notIn := func(key string, vs ...string) req {
		return req{Key: key, Operator: metav1.LabelSelectorOpNotIn, Values: vs}
	}
	ab := map[string]string{"a": "1", "b": "2"}
	for _, tc := range []struct {
		name string
		a, b *metav1.LabelSelector
		want bool
	}{
		{"same labels", sel(ab), sel(map[string]string{"b": "2", "a": "1"}), true},
		{"empty and nil labels", sel(map[string]string{}), sel(nil), true},
		{"reordered expressions", sel(nil, exists("k"), in("x", "1")), sel(nil, in("x", "1"), exists("k")), true},
		{"reordered in values", sel(nil, in("k", "a", "b", "c")), sel(nil, in("k", "c", "a", "b")), true},
		{"reordered not in values", sel(nil, notIn("k", "a", "b")), sel(nil, notIn("k", "b", "a")), true},
		{"duplicate value", sel(nil, in("k", "a", "a")), sel(nil, in("k", "a")), true},
		{"labels and expressions", sel(ab, in("k", "a", "b")), sel(ab, in("k", "b", "a")), true},

		{"other label value", sel(ab), sel(map[string]string{"a": "1", "b": "3"}), false},
		{"extra label", sel(ab), sel(map[string]string{"a": "1"}), false},
		{"label is not expression", sel(map[string]string{"k": "a"}), sel(nil, in("k", "a")), false},
		{"in is not not in", sel(nil, in("k", "a")), sel(nil, notIn("k", "a")), false},
		{"exists is not does not exist", sel(nil, exists("k")),
			sel(nil, req{Key: "k", Operator: metav1.LabelSelectorOpDoesNotExist}), false},
		{"extra value", sel(nil, in("k", "a")), sel(nil, in("k", "a", "b")), false},
		{"split values", sel(nil, in("k", "a"), in("k", "b")), sel(nil, in("k", "a", "b")), false},
		{"extra expression", sel(nil, exists("k")), sel(nil, exists("k"), exists("j")), false},
		{"other key", sel(nil, in("k", "a")), sel(nil, in("j", "a")), false},
		{"nil", nil, sel(nil), false},

		{"in with no values", sel(nil, in("k")), sel(nil, in("k")), false},
		{"exists with values", sel(nil, req{Key: "k", Operator: metav1.LabelSelectorOpExists, Values: []string{"a"}}),
			sel(nil, req{Key: "k", Operator: metav1.LabelSelectorOpExists, Values: []string{"a"}}), false},
		{"unknown operator", sel(nil, req{Key: "k", Operator: "Matches"}), sel(nil, req{Key: "k", Operator: "Matches"}), false},
		{"bad label key", sel(map[string]string{"bad key": "a"}), sel(map[string]string{"bad key": "a"}), false},
		{"bad label value", sel(map[string]string{"k": joined}), sel(map[string]string{"k": joined}), false},
		{"bad expression key", sel(nil, exists("a/b/c")), sel(nil, exists("a/b/c")), false},
		{"joined values", sel(nil, in("k", joined)), sel(nil, in("k", joined)), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var a, b *metav1.LabelSelector
			if tc.a != nil {
				a = tc.a.DeepCopy()
			}
			if tc.b != nil {
				b = tc.b.DeepCopy()
			}
			if sameSelector(tc.a, tc.b) != tc.want || sameSelector(tc.b, tc.a) != tc.want {
				t.Fatalf("sameSelector returned the wrong result: want %t", tc.want)
			}
			if !equality.Semantic.DeepEqual(a, tc.a) || !equality.Semantic.DeepEqual(b, tc.b) {
				t.Fatal("sameSelector changed its input")
			}
		})
	}
}
