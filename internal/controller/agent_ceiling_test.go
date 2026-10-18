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

package controller

import (
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/yaml"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
)

// Rule words for the tests.
const (
	verbGet    = "get"
	resPods    = "pods"
	resSecrets = "secrets"
)

func TestCeilingMatchesTheClusterRole(t *testing.T) {
	data, err := os.ReadFile("../../config/rbac/agent_ceiling_role.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var role rbacv1.ClusterRole
	if err := yaml.Unmarshal([]byte(strings.Split(string(data), "\n---")[0]), &role); err != nil {
		t.Fatal(err)
	}
	if role.Kind != "ClusterRole" || !reflect.DeepEqual(role.Rules, []rbacv1.PolicyRule{ceiling}) {
		t.Fatalf("the ceiling in the code is not the same as the agent-ceiling ClusterRole: %+v", role.Rules)
	}
}

func TestInvalid(t *testing.T) {
	rule := func(groups, resources, verbs []string) rbacv1.PolicyRule {
		return rbacv1.PolicyRule{APIGroups: groups, Resources: resources, Verbs: verbs}
	}
	core := []string{""}
	for _, c := range []struct {
		name  string
		rules []rbacv1.PolicyRule
		bad   bool
	}{
		{"a-ok", []rbacv1.PolicyRule{rule(core, []string{resPods, "pods/log"}, []string{verbGet, "list"})}, false},
		{"no-rules", nil, false},
		{resSecrets, []rbacv1.PolicyRule{rule(core, []string{resSecrets}, []string{verbGet})}, true},
		{"star-verb", []rbacv1.PolicyRule{rule(core, []string{resPods}, []string{"*"})}, true},
		{"star-resource", []rbacv1.PolicyRule{rule(core, []string{"*"}, []string{verbGet})}, true},
		{"apps", []rbacv1.PolicyRule{rule([]string{"apps"}, []string{resPods}, []string{verbGet})}, true},
		{"no-group", []rbacv1.PolicyRule{rule(nil, []string{resPods}, []string{verbGet})}, true},
		{"delete", []rbacv1.PolicyRule{rule(core, []string{resPods}, []string{"delete"})}, true},
		{"url", []rbacv1.PolicyRule{{Verbs: []string{verbGet}, NonResourceURLs: []string{"/"}}}, true},
		{strings.Repeat("n", 61), nil, true},
	} {
		a := &corev1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: c.name},
			Spec: corev1alpha1.AgentSpec{Rules: c.rules}}
		if got := invalid(a) != ""; got != c.bad {
			t.Errorf("%s: invalid is %v, want %v", c.name, got, c.bad)
		}
	}
}

func TestPermanent(t *testing.T) {
	gr := schema.GroupResource{Group: "batch", Resource: "jobs"}
	for _, c := range []struct {
		err  error
		want bool
	}{
		{apierrors.NewRequestEntityTooLargeError("too large"), true},
		{apierrors.NewForbidden(gr, "kd-a", errors.New("no")), true},
		{apierrors.NewBadRequest("bad"), true},
		{apierrors.NewConflict(gr, "kd-a", errors.New("changed")), false},
		{apierrors.NewTooManyRequests("slow down", 1), false},
		{apierrors.NewTimeoutError("timeout", 1), false},
		{apierrors.NewInternalError(errors.New("down")), false},
		{errors.New("connection refused"), false},
	} {
		if got := permanent(c.err); got != c.want {
			t.Errorf("permanent(%v) is %v, want %v", c.err, got, c.want)
		}
	}
}
