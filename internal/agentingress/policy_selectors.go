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
	"slices"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
)

// sameSelector is true if got and want are valid and have the same approved selector terms. It
// compares the matchLabels as maps, the matchExpressions as a set, and the values of each expression
// as a set. It keeps matchLabels separate from an equivalent In expression, so a selector in the
// other form is never the same even if it selects the same pods. A nil or malformed selector is
// never the same.
func sameSelector(got, want *metav1.LabelSelector) bool {
	if !validSelector(got) || !validSelector(want) || len(got.MatchLabels) != len(want.MatchLabels) {
		return false
	}
	for k, v := range want.MatchLabels {
		if gv, ok := got.MatchLabels[k]; !ok || gv != v {
			return false
		}
	}
	return subsetExpressions(got.MatchExpressions, want.MatchExpressions) &&
		subsetExpressions(want.MatchExpressions, got.MatchExpressions)
}

// subsetExpressions is true if each expression in a has an equal expression in b.
func subsetExpressions(a, b []metav1.LabelSelectorRequirement) bool {
	for _, x := range a {
		if !slices.ContainsFunc(b, func(y metav1.LabelSelectorRequirement) bool {
			return x.Key == y.Key && x.Operator == y.Operator && sameSet(x.Values, y.Values)
		}) {
			return false
		}
	}
	return true
}

// sameSet is true if a and b have the same members.
func sameSet(a, b []string) bool {
	return subset(a, b) && subset(b, a)
}

// subset is true if each member of a is in b.
func subset(a, b []string) bool {
	for _, x := range a {
		if !slices.Contains(b, x) {
			return false
		}
	}
	return true
}

// validSelector is true if s is not nil and has valid label keys, values and operators. In and
// NotIn need one or more values. Exists and DoesNotExist need no values.
func validSelector(s *metav1.LabelSelector) bool {
	if s == nil {
		return false
	}
	for k, v := range s.MatchLabels {
		if len(validation.IsQualifiedName(k)) > 0 || len(validation.IsValidLabelValue(v)) > 0 {
			return false
		}
	}
	for _, e := range s.MatchExpressions {
		if len(validation.IsQualifiedName(e.Key)) > 0 {
			return false
		}
		switch e.Operator {
		case metav1.LabelSelectorOpIn, metav1.LabelSelectorOpNotIn:
			if len(e.Values) == 0 {
				return false
			}
		case metav1.LabelSelectorOpExists, metav1.LabelSelectorOpDoesNotExist:
			if len(e.Values) > 0 {
				return false
			}
		default:
			return false
		}
		for _, v := range e.Values {
			if len(validation.IsValidLabelValue(v)) > 0 {
				return false
			}
		}
	}
	return true
}
