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

package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	testTenant   = "team a/b"
	otherBudget  = "operator-budget"
	pathBudgetUp = "/budget/update"
	// The LiteLLM field names that the fake uses more than one time.
	fieldBudgetID = "budget_id"
	fieldSpend    = "spend"
	fieldMax      = "max_budget"
	fieldDuration = "budget_duration"
)

// customerFake is a small LiteLLM with budgets and customers, as v1.104.2 answers.
type customerFake struct {
	mu        sync.Mutex
	budgets   map[string]map[string]any
	customers map[string]map[string]any // user_id -> {fieldBudgetID: ..., fieldSpend: ...}
	calls     []string
	logQuery  map[string]string
	total     any
	capped    bool
	// raceBudget and raceCustomer make the first create fail as if a second writer made the row.
	raceBudget, raceCustomer bool
	// wrongRoute makes each customer path answer with the 404 of a wrong route.
	wrongRoute bool
	resetAt    string
}

func newCustomerFake() *customerFake {
	return &customerFake{budgets: map[string]map[string]any{}, customers: map[string]map[string]any{},
		total: 0, resetAt: "2026-11-26T00:00:00Z"}
}

func (f *customerFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, r.URL.Path)
	var b map[string]any
	_ = json.NewDecoder(r.Body).Decode(&b)
	fail := func(status int, body string) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
	if f.wrongRoute && strings.HasPrefix(r.URL.Path, "/customer/") {
		fail(http.StatusNotFound, routeNotFound)
		return
	}
	switch r.URL.Path {
	case "/budget/info":
		rows := []map[string]any{}
		for _, id := range b["budgets"].([]any) {
			if row, ok := f.budgets[id.(string)]; ok {
				rows = append(rows, row)
			}
		}
		_ = json.NewEncoder(w).Encode(rows)
	case "/budget/new":
		id := b[fieldBudgetID].(string)
		if _, ok := f.budgets[id]; ok || f.raceBudget {
			f.raceBudget = false
			f.budgets[id] = map[string]any{fieldBudgetID: id, fieldDuration: "7d", "budget_reset_at": f.resetAt}
			fail(http.StatusBadRequest, `{"detail":{"error":"Budget with id '`+id+`' already exists."}}`)
			return
		}
		b["budget_reset_at"] = f.resetAt
		f.budgets[id] = b
		_, _ = w.Write([]byte(`{}`))
	case pathBudgetUp:
		// LiteLLM answers 200 with null for a budget that does not exist.
		if row, ok := f.budgets[b[fieldBudgetID].(string)]; ok {
			maps.Copy(row, b)
		}
		_, _ = w.Write([]byte(`null`))
	case "/budget/delete":
		delete(f.budgets, b["id"].(string))
		_, _ = w.Write([]byte(`null`))
	case "/customer/info":
		id := r.URL.Query().Get("end_user_id")
		c, ok := f.customers[id]
		if !ok {
			fail(http.StatusNotFound, `{"error":{"message":"End User Id=`+id+` does not exist in db","type":"not_found","param":"end_user_id","code":"404"}}`)
			return
		}
		out := map[string]any{"user_id": id, fieldSpend: c[fieldSpend], fieldBudgetID: c[fieldBudgetID]}
		if bid, ok := c[fieldBudgetID].(string); ok {
			out["litellm_budget_table"] = f.budgets[bid]
		}
		_ = json.NewEncoder(w).Encode(out)
	case "/customer/new":
		id := b["user_id"].(string)
		if _, ok := f.customers[id]; ok || f.raceCustomer {
			f.raceCustomer = false
			if !ok {
				f.customers[id] = map[string]any{fieldSpend: 0.5}
			}
			fail(http.StatusBadRequest, `{"error":{"message":"Customer already exists, passed user_id=`+id+`.","type":"bad_request","param":"user_id","code":"400"}}`)
			return
		}
		f.customers[id] = map[string]any{fieldSpend: 0.0, fieldBudgetID: b[fieldBudgetID]}
		_, _ = w.Write([]byte(`{}`))
	case "/customer/update":
		f.customers[b["user_id"].(string)][fieldBudgetID] = b[fieldBudgetID]
		_, _ = w.Write([]byte(`{}`))
	case "/customer/delete":
		id := b["user_ids"].([]any)[0].(string)
		if _, ok := f.customers[id]; !ok {
			fail(http.StatusNotFound, `{"error":{"message":"End User Id(s)=`+id+` do not exist in db","type":"not_found","param":"user_ids","code":"404"}}`)
			return
		}
		delete(f.customers, id)
		_, _ = w.Write([]byte(`{}`))
	case "/spend/logs/v2":
		f.logQuery = map[string]string{}
		for k, v := range r.URL.Query() {
			f.logQuery[k] = v[0]
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"total": f.total, "total_is_capped": f.capped, "data": []any{}})
	default:
		fail(http.StatusNotFound, routeNotFound)
	}
}

func (f *customerFake) count(path string) int {
	n := 0
	for _, c := range f.calls {
		if c == path {
			n++
		}
	}
	return n
}

func customerClient(t *testing.T, f *customerFake) *LiteLLM {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	l := NewLiteLLM(srv.URL, "master")
	l.Retries = 0
	return l
}

func TestSetCustomer(t *testing.T) {
	ctx := context.Background()
	f := newCustomerFake()
	l := customerClient(t, f)
	spec := CustomerSpec{ID: testTenant, Budget: 2.5, Period: 24 * time.Hour, RequestsPerMinute: 300}
	id := customerBudgetID(testTenant)
	if strings.ContainsAny(id, " /") || !strings.HasPrefix(id, "kd-tenant-") || id == customerBudgetID("other") {
		t.Fatalf("budget ID %q", id)
	}
	if err := l.SetCustomer(ctx, spec); err != nil {
		t.Fatal(err)
	}
	b := f.budgets[id]
	if b[fieldMax] != 2.5 || b[fieldDuration] != "1d" || b["rpm_limit"] != 300.0 ||
		f.customers[testTenant][fieldBudgetID] != id {
		t.Errorf("budget %v, customer %v", b, f.customers[testTenant])
	}
	// The same spec writes nothing.
	writes := len(f.calls) - f.count("/budget/info") - f.count("/customer/info")
	if err := l.SetCustomer(ctx, spec); err != nil {
		t.Fatal(err)
	}
	if got := len(f.calls) - f.count("/budget/info") - f.count("/customer/info"); got != writes {
		t.Errorf("the same spec made %d writes", got-writes)
	}
	// A spec with no limit removes the limits with a JSON null, and an hour uses the unit h.
	if err := l.SetCustomer(ctx, CustomerSpec{ID: testTenant, Period: time.Hour}); err != nil {
		t.Fatal(err)
	}
	if b[fieldMax] != nil || b["rpm_limit"] != nil || b[fieldDuration] != "1h" || f.count(pathBudgetUp) != 1 {
		t.Errorf("budget after the change: %v", b)
	}
	for _, bad := range []CustomerSpec{{Period: time.Hour}, {ID: "a"}, {ID: "a", Period: time.Hour, Budget: -1},
		{ID: "a", Period: time.Hour, RequestsPerMinute: -1}} {
		if l.SetCustomer(ctx, bad) == nil {
			t.Errorf("%+v is accepted", bad)
		}
	}
}

// The gateway makes a customer with no budget at the first call of a tenant label, and an
// operator can give a customer a different budget. SetCustomer gives the customer the Koodam
// budget, keeps the spend, and does not change the other budget.
func TestSetCustomerExisting(t *testing.T) {
	ctx := context.Background()
	spec := CustomerSpec{ID: testTenant, Budget: 1, Period: time.Hour}
	for name, row := range map[string]map[string]any{
		"no budget":    {fieldSpend: 0.75},
		"other budget": {fieldSpend: 0.75, fieldBudgetID: otherBudget},
	} {
		f := newCustomerFake()
		f.budgets[otherBudget] = map[string]any{fieldBudgetID: otherBudget, fieldMax: 9.0}
		f.customers[testTenant] = row
		if err := customerClient(t, f).SetCustomer(ctx, spec); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if row[fieldBudgetID] != customerBudgetID(testTenant) || row[fieldSpend] != 0.75 ||
			f.budgets[otherBudget][fieldMax] != 9.0 || len(f.budgets[otherBudget]) != 2 {
			t.Errorf("%s: customer %v, other budget %v", name, row, f.budgets[otherBudget])
		}
	}
	// A second writer makes the budget or the customer at the same time.
	f := newCustomerFake()
	f.raceBudget, f.raceCustomer = true, true
	if err := customerClient(t, f).SetCustomer(ctx, spec); err != nil {
		t.Fatal(err)
	}
	b := f.budgets[customerBudgetID(testTenant)]
	if b[fieldDuration] != "1h" || b[fieldMax] != 1.0 || f.customers[testTenant][fieldBudgetID] != b[fieldBudgetID] {
		t.Errorf("after the race: budget %v, customer %v", b, f.customers[testTenant])
	}
}

func TestCustomerUsage(t *testing.T) {
	ctx := context.Background()
	f := newCustomerFake()
	l := customerClient(t, f)
	if _, err := l.CustomerUsage(ctx, testTenant); !errors.Is(err, ErrCustomerNotFound) {
		t.Fatalf("missing customer: %v", err)
	}
	// A customer with no Koodam budget has no period.
	f.customers[testTenant] = map[string]any{fieldSpend: 0.1}
	if _, err := l.CustomerUsage(ctx, testTenant); !errors.Is(err, ErrCustomerNotFound) {
		t.Fatalf("customer with no budget: %v", err)
	}
	if err := l.SetCustomer(ctx, CustomerSpec{ID: testTenant, Budget: 1, Period: 24 * time.Hour}); err != nil {
		t.Fatal(err)
	}
	f.customers[testTenant][fieldSpend], f.total = 0.25, 7
	u, err := l.CustomerUsage(ctx, testTenant)
	want := CustomerUsage{Spend: 0.25, Requests: 7, PeriodEnd: time.Date(2026, 11, 26, 0, 0, 0, 0, time.UTC)}
	if err != nil || u != want {
		t.Fatalf("usage %+v, error %v", u, err)
	}
	// The filter starts one period before the end of the period, and it counts only successful calls.
	q := f.logQuery
	if q["end_user"] != testTenant || q["status_filter"] != "success" || q["start_date"] != "2026-11-25 00:00:00" ||
		q["page_size"] != "1" || q["end_date"] <= time.Now().UTC().Format(spendLogTime) {
		t.Errorf("spend log query %v", q)
	}
	// The gateway stops the count at its maximum. The count is then a minimum, and the flag shows it.
	f.total, f.capped = 10000, true
	if u, err := l.CustomerUsage(ctx, testTenant); err != nil || u.Requests != 10000 || !u.RequestsCapped {
		t.Fatalf("capped usage %+v, error %v", u, err)
	}
	f.capped = false
	for name, change := range map[string]func(){
		"no total":       func() { f.total = nil },
		"negative total": func() { f.total = -1 },
		"bad period":     func() { f.total = 1; f.budgets[customerBudgetID(testTenant)][fieldDuration] = "1mo" },
		"no reset time": func() {
			f.budgets[customerBudgetID(testTenant)][fieldDuration] = "1d"
			delete(f.budgets[customerBudgetID(testTenant)], "budget_reset_at")
		},
	} {
		change()
		if _, err := l.CustomerUsage(ctx, testTenant); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestDeleteCustomer(t *testing.T) {
	ctx := context.Background()
	f := newCustomerFake()
	l := customerClient(t, f)
	if err := l.SetCustomer(ctx, CustomerSpec{ID: testTenant, Period: time.Hour}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		// The second delete finds no customer, and that is not an error.
		if err := l.DeleteCustomer(ctx, testTenant); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.customers) != 0 || len(f.budgets) != 0 {
		t.Errorf("after the delete: customers %v, budgets %v", f.customers, f.budgets)
	}
	if l.DeleteCustomer(ctx, "") == nil {
		t.Error("an empty ID is accepted")
	}
	// The 404 of a wrong route is not proof that the customer is missing.
	f.wrongRoute = true
	if err := l.DeleteCustomer(ctx, testTenant); err == nil || f.count("/budget/delete") != 2 {
		t.Errorf("wrong route: error %v, %d budget deletes", err, f.count("/budget/delete"))
	}
	if _, err := l.CustomerUsage(ctx, testTenant); err == nil || errors.Is(err, ErrCustomerNotFound) {
		t.Errorf("wrong route, usage: %v", err)
	}
	if err := l.SetCustomer(ctx, CustomerSpec{ID: testTenant, Period: time.Hour}); err == nil || len(f.customers) != 0 {
		t.Errorf("wrong route, set: %v", err)
	}
}

func TestBudgetDuration(t *testing.T) {
	for period, want := range map[time.Duration]string{
		time.Hour: "1h", 24 * time.Hour: "1d", 48 * time.Hour: "2d", 90 * time.Minute: "5400s", 36 * time.Hour: "36h",
	} {
		got := budgetDuration(period)
		back, ok := parseBudgetDuration(got)
		if got != want || !ok || back != period {
			t.Errorf("%v: %q, back %v %v", period, got, back, ok)
		}
	}
	if d, ok := parseBudgetDuration("30m"); !ok || d != 30*time.Minute {
		t.Errorf("30m: %v %v", d, ok)
	}
	for _, bad := range []string{"", "d", "1mo", "0d", "-1h", "1.5h", "9999999999999d", "1 d"} {
		if _, ok := parseBudgetDuration(bad); ok {
			t.Errorf("%q is accepted", bad)
		}
	}
}
