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
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	corev1beta1 "github.com/mohanraj00/koodam/api/v1beta1"
)

// byFlag is the group flag of koodam usage.
const byFlag = "--by"

func tenantQuota(name, tenant, app, spend string, requests int64, ready, exceeded bool) *corev1beta1.TenantQuota {
	status := func(on bool) metav1.ConditionStatus {
		if on {
			return metav1.ConditionTrue
		}
		return metav1.ConditionFalse
	}
	q := &corev1beta1.TenantQuota{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNS},
		Spec: corev1beta1.TenantQuotaSpec{Tenant: tenant},
		Status: corev1beta1.TenantQuotaStatus{Requests: requests, Spend: spend,
			PeriodEnd: &metav1.Time{Time: time.Date(2026, 11, 26, 0, 0, 0, 0, time.UTC)},
			Conditions: []metav1.Condition{
				{Type: corev1beta1.TenantQuotaReady, Status: status(ready)},
				{Type: corev1beta1.TenantQuotaExceeded, Status: status(exceeded)},
			}}}
	if app != "" {
		q.Spec.AppRef = &corev1beta1.AppReference{Name: app}
	}
	return q
}

func TestUsageByTenant(t *testing.T) {
	other := tenantQuota("other-namespace", "zeta", "", "9", 900, true, false)
	other.Namespace = otherName
	noEnd := tenantQuota("no-end", "delta", "", "0", 0, true, false)
	noEnd.Status.PeriodEnd = nil
	// The gateway stopped the count of the requests below the limit, so the quota state is not known.
	capped := tenantQuota("capped", "epsilon", "", "0.0100", 3, true, false)
	capped.Status.Conditions[1].Status = metav1.ConditionUnknown
	capped.Status.Conditions[1].Reason = "RequestCountCapped"
	noMark := tenantQuota("no-mark", "eta", "", "0", 0, true, false)
	noMark.Status.Conditions = noMark.Status.Conditions[:1]
	kube := newKube(t, nil,
		tenantQuota("b", "beta", "storefront", "0.0300", 12, true, true),
		tenantQuota("a", "alpha", "", "0.0150", 5, true, false),
		// A tenant label with a line break and an escape sequence goes into quotes.
		tenantQuota("c", "evil\n\x1b[2Jtotal", "", "0.0050", 1, true, false),
		tenantQuota("duplicate", "alpha", "", "5", 500, false, false),
		tenantQuota("bad-spend", "gamma", "", "invalid", 7, true, false), noEnd, capped, noMark, other)
	old := outputProgram
	t.Cleanup(func() { outputProgram = old })
	outputProgram = func(*cobra.Command, string, string, ...string) ([]byte, error) {
		t.Fatal("tenant usage must not use SSH")
		return nil, nil
	}
	out, errOut, err := runUsageBy(func() (*env, error) {
		return &env{kube: kube, namespace: testNS}, nil
	}, byFlag, byTenant)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"TENANT APP REQUESTS SPEND EXCEEDED PERIOD-END SOURCE",
		`"evil\n\x1b[2Jtotal" (all) 1 0.0050 USD no 2026-11-26T00:00:00Z source: gateway`,
		"alpha (all) 5 0.0150 USD no 2026-11-26T00:00:00Z source: gateway",
		"beta storefront 12 0.0300 USD yes 2026-11-26T00:00:00Z source: gateway",
		"delta (all) 0 0.0000 USD no - source: gateway",
		"epsilon (all) 3 0.0100 USD unknown 2026-11-26T00:00:00Z source: gateway",
		"eta (all) 0 0.0000 USD unknown 2026-11-26T00:00:00Z source: gateway",
		"total 21 0.0600 USD source: gateway",
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for i := range lines {
		lines[i] = strings.Join(strings.Fields(lines[i]), " ")
	}
	if !slices.Equal(lines, want) {
		t.Errorf("table:\n%s\nwant:\n%s", out, strings.Join(want, "\n"))
	}
	if strings.ContainsRune(out, '\x1b') {
		t.Error("the table has an escape character of a tenant label")
	}
	if wantNote := "note: TenantQuotas that are not Ready: 1. The cause is a gateway error, or an older " +
		"TenantQuota with the same tenant label. They are not in the table.\n" +
		"note: TenantQuotas with a spend that is not a number: 1. They are not in the table.\n" +
		"note: TenantQuotas with an unknown quota state: 2. If the reason is RequestCountCapped, " +
		"the gateway stopped the request count, and REQUESTS is a minimum.\n"; errOut != wantNote {
		t.Errorf("stderr: got %q, want %q", errOut, wantNote)
	}
}

func TestUsageByTenantWithNoUsage(t *testing.T) {
	kube := newKube(t, nil, tenantQuota("pending", "alpha", "", "", 0, false, false))
	out, errOut, err := runUsageBy(func() (*env, error) {
		return &env{kube: kube, namespace: testNS}, nil
	}, byFlag, byTenant)
	if err != nil || out != "No tenant usage in the TenantQuotas of namespace "+testNS+".\n" ||
		!strings.HasPrefix(errOut, "note: TenantQuotas that are not Ready: 1.") {
		t.Errorf("got out %q, stderr %q, error %v", out, errOut, err)
	}
}
