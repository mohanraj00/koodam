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
	"fmt"
	"io"
	"math"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
	"unicode"

	"k8s.io/apimachinery/pkg/api/meta"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
)

// tenantText gives back a tenant label or an App name for the table. The API server accepts each
// character in a tenant label. A text with a character that is not printable goes into quotes
// with escapes, so it cannot change the lines of the terminal.
func tenantText(s string) string {
	if strings.ContainsFunc(s, func(r rune) bool { return !unicode.IsPrint(r) }) {
		return strconv.Quote(s)
	}
	return s
}

// printTenantUsage writes 1 line for each TenantQuota that has the use of its tenant, and 1 total
// line. The use is the status of the TenantQuota: the count of the gateway for the current period.
func printTenantUsage(out, errOut io.Writer, quotas []corev1alpha1.TenantQuota, namespace string) error {
	type row struct {
		tenant, app, end string
		requests         int64
		spend            float64
		exceeded         bool
	}
	var rows []row
	var total row
	var notReady, invalid int
	for i := range quotas {
		q := &quotas[i]
		if !meta.IsStatusConditionTrue(q.Status.Conditions, corev1alpha1.TenantQuotaReady) {
			notReady++
			continue
		}
		spend, err := strconv.ParseFloat(q.Status.Spend, 64)
		if err != nil || math.IsNaN(spend) || math.IsInf(spend, 0) {
			invalid++
			continue
		}
		r := row{tenant: tenantText(q.Spec.Tenant), app: "(all)", end: "-", requests: q.Status.Requests, spend: spend,
			exceeded: meta.IsStatusConditionTrue(q.Status.Conditions, corev1alpha1.TenantQuotaExceeded)}
		if q.Spec.AppRef != nil {
			r.app = tenantText(q.Spec.AppRef.Name)
		}
		if q.Status.PeriodEnd != nil {
			r.end = q.Status.PeriodEnd.UTC().Format(time.RFC3339)
		}
		rows = append(rows, r)
		total.requests += r.requests
		total.spend += r.spend
	}
	if notReady > 0 {
		_, _ = fmt.Fprintf(errOut, "note: TenantQuotas that are not Ready: %d. The cause is a gateway error, or an older "+
			"TenantQuota with the same tenant label. They are not in the table.\n", notReady)
	}
	if invalid > 0 {
		_, _ = fmt.Fprintf(errOut, "note: TenantQuotas with a spend that is not a number: %d. "+
			"They are not in the table.\n", invalid)
	}
	if len(rows) == 0 {
		_, err := fmt.Fprintf(out, "No tenant usage in the TenantQuotas of namespace %s.\n", namespace)
		return err
	}
	slices.SortFunc(rows, func(a, b row) int { return strings.Compare(a.tenant, b.tenant) })
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "TENANT\tAPP\tREQUESTS\tSPEND\tEXCEEDED\tPERIOD-END\tSOURCE")
	for _, r := range rows {
		exceeded := "no"
		if r.exceeded {
			exceeded = "yes"
		}
		_, _ = fmt.Fprintf(w, "%s\t%s\t%d\t%.4f USD\t%s\t%s\tsource: gateway\n",
			r.tenant, r.app, r.requests, r.spend, exceeded, r.end)
	}
	_, _ = fmt.Fprintf(w, "total\t\t%d\t%.4f USD\t\t\tsource: gateway\n", total.requests, total.spend)
	return w.Flush()
}
