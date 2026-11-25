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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// CustomerSpec is the gateway customer of one tenant label. The gateway counts each model call
// that has the tenant label in its user field for this customer.
type CustomerSpec struct {
	// ID is the tenant label.
	ID string
	// Budget is the maximum spend of the customer in one period. 0 gives no spend limit.
	Budget float64
	// Period is the length of one budget period. It is 1 second or more.
	Period time.Duration
	// RequestsPerMinute is the maximum count of model calls in one minute. 0 gives no limit.
	RequestsPerMinute int64
}

// CustomerUsage is the gateway count for one customer in its current period.
type CustomerUsage struct {
	// Spend is the spend of the customer in the current period.
	Spend float64
	// Requests is the count of successful model calls of the customer in the current period.
	Requests int64
	// PeriodEnd is the time when the gateway starts the next period.
	PeriodEnd time.Time
}

// ErrCustomerNotFound is the CustomerUsage error when the gateway has no customer, or the
// customer has no budget period.
var ErrCustomerNotFound = errors.New("gateway: customer not found")

// Customers is a model gateway with a customer for each tenant label.
type Customers interface {
	// SetCustomer makes the customer with its limits. If the customer exists, it sets the limits.
	SetCustomer(ctx context.Context, spec CustomerSpec) error
	// CustomerUsage returns the count of the customer in its current period. If the customer is
	// missing, it returns ErrCustomerNotFound.
	CustomerUsage(ctx context.Context, id string) (CustomerUsage, error)
	// DeleteCustomer removes the customer and its limits. A missing customer returns nil.
	DeleteCustomer(ctx context.Context, id string) error
}

// customerBudgetID is the ID of the LiteLLM budget that Koodam owns for a tenant label. The ID
// has a hash, because a tenant label can have characters that are not safe in an ID.
func customerBudgetID(id string) string {
	h := sha256.Sum256([]byte(id))
	return "kd-tenant-" + hex.EncodeToString(h[:16])
}

type budgetRow struct {
	ID       string     `json:"budget_id"`
	Max      *float64   `json:"max_budget"`
	Duration *string    `json:"budget_duration"`
	RPM      *int64     `json:"rpm_limit"`
	ResetAt  *time.Time `json:"budget_reset_at"`
}

type customerInfo struct {
	Spend    float64    `json:"spend"`
	BudgetID *string    `json:"budget_id"`
	Budget   *budgetRow `json:"litellm_budget_table"`
}

// body gives back the LiteLLM budget fields of the spec. A limit of 0 is a JSON null, which
// removes the limit of an existing budget.
func (s CustomerSpec) body() map[string]any {
	body := map[string]any{"budget_id": customerBudgetID(s.ID), "max_budget": nil, "rpm_limit": nil,
		"budget_duration": budgetDuration(s.Period)}
	if s.Budget > 0 {
		body["max_budget"] = s.Budget
	}
	if s.RequestsPerMinute > 0 {
		body["rpm_limit"] = s.RequestsPerMinute
	}
	return body
}

// budgetDuration gives back the LiteLLM duration of a period. A period of full days or full hours
// uses the unit d or h, because the gateway then starts each period at 00:00 UTC or at the full
// hour.
func budgetDuration(period time.Duration) string {
	switch {
	case period%(24*time.Hour) == 0:
		return fmt.Sprintf("%dd", int64(period/(24*time.Hour)))
	case period%time.Hour == 0:
		return fmt.Sprintf("%dh", int64(period/time.Hour))
	}
	return fmt.Sprintf("%ds", int64(period/time.Second))
}

// parseBudgetDuration reads a LiteLLM duration with the unit s, m, h or d.
func parseBudgetDuration(v string) (time.Duration, bool) {
	units := map[byte]time.Duration{'s': time.Second, 'm': time.Minute, 'h': time.Hour, 'd': 24 * time.Hour}
	if len(v) < 2 || len(v) > 12 {
		return 0, false
	}
	unit, ok := units[v[len(v)-1]]
	n, err := strconv.ParseInt(v[:len(v)-1], 10, 64)
	if !ok || err != nil || n < 1 || n > math.MaxInt64/int64(unit) {
		return 0, false
	}
	return time.Duration(n) * unit, true
}

// same tells if the budget row has the limits of the spec.
func (s CustomerSpec) same(b budgetRow) bool {
	budget, rpm := 0.0, int64(0)
	if b.Max != nil {
		budget = *b.Max
	}
	if b.RPM != nil {
		rpm = *b.RPM
	}
	return b.Duration != nil && *b.Duration == budgetDuration(s.Period) &&
		math.Abs(budget-max(s.Budget, 0)) < 1e-12 && rpm == max(s.RequestsPerMinute, 0)
}

// isStatus tells if err is a LiteLLM answer with this HTTP status.
func isStatus(err error, status int) bool {
	he, ok := errors.AsType[*HTTPError](err)
	return ok && he.Status == status
}

// customerMissing is true only for the LiteLLM error for a missing customer: HTTP 404 with
// error.type "not_found". A wrong route or a proxy in front of LiteLLM can give a different 404,
// and that is not proof that the customer is missing.
func customerMissing(err error) bool {
	he, ok := errors.AsType[*HTTPError](err)
	return ok && he.customerNotFound
}

// isCustomerNotFound reads the full body, before do cuts it for the error message.
func isCustomerNotFound(data []byte) bool {
	var body struct {
		Error *struct {
			Type  string `json:"type"`
			Param string `json:"param"`
		} `json:"error"`
	}
	if json.Unmarshal(data, &body) != nil || body.Error == nil {
		return false
	}
	return body.Error.Type == "not_found" && (body.Error.Param == "end_user_id" || body.Error.Param == "user_ids")
}

// SetCustomer implements Customers. Koodam owns one LiteLLM budget for each tenant label, and
// it changes no other budget. The customer keeps its spend when it gets this budget.
func (l *LiteLLM) SetCustomer(ctx context.Context, s CustomerSpec) error {
	if s.ID == "" || s.Period < time.Second || s.Budget < 0 || math.IsNaN(s.Budget) || math.IsInf(s.Budget, 0) ||
		s.RequestsPerMinute < 0 {
		return errors.New("litellm: a customer ID, a period of 1s or more and limits of 0 or more are required")
	}
	budgetID := customerBudgetID(s.ID)
	var rows []budgetRow
	if err := l.do(ctx, http.MethodPost, "/budget/info", map[string]any{"budgets": []string{budgetID}}, "", &rows); err != nil {
		return err
	}
	switch {
	case len(rows) == 0:
		err := l.do(ctx, http.MethodPost, "/budget/new", s.body(), "", nil)
		// A second controller can make the budget at the same time. Then set its limits.
		if isStatus(err, http.StatusBadRequest) {
			err = l.do(ctx, http.MethodPost, "/budget/update", s.body(), "", nil)
		}
		if err != nil {
			return err
		}
	case !s.same(rows[0]):
		if err := l.do(ctx, http.MethodPost, "/budget/update", s.body(), "", nil); err != nil {
			return err
		}
	}
	info, err := l.customer(ctx, s.ID)
	link := map[string]string{"user_id": s.ID, "budget_id": budgetID}
	switch {
	case errors.Is(err, ErrCustomerNotFound):
		err = l.do(ctx, http.MethodPost, "/customer/new", link, "", nil)
		// The gateway makes a customer with no budget at the first model call of a tenant label.
		if isStatus(err, http.StatusBadRequest) {
			err = l.do(ctx, http.MethodPost, "/customer/update", link, "", nil)
		}
		return err
	case err != nil:
		return err
	case info.BudgetID == nil || *info.BudgetID != budgetID:
		return l.do(ctx, http.MethodPost, "/customer/update", link, "", nil)
	}
	return nil
}

// customer reads the customer row. A missing customer returns ErrCustomerNotFound.
func (l *LiteLLM) customer(ctx context.Context, id string) (customerInfo, error) {
	var info customerInfo
	err := l.do(ctx, http.MethodGet, "/customer/info?end_user_id="+url.QueryEscape(id), nil, "", &info)
	if customerMissing(err) {
		return info, fmt.Errorf("%w: %w", ErrCustomerNotFound, err)
	}
	return info, err
}

// spendLogTime is the time format of the LiteLLM spend log filter. The gateway reads it as UTC.
const spendLogTime = "2006-01-02 15:04:05"

// CustomerUsage implements Customers. The spend is the count of the gateway for the current
// budget period. The requests are the successful calls in the spend log from the start of that
// period. The gateway gives only the count, so the answer is small for each count of calls.
func (l *LiteLLM) CustomerUsage(ctx context.Context, id string) (CustomerUsage, error) {
	info, err := l.customer(ctx, id)
	if err != nil {
		return CustomerUsage{}, err
	}
	b := info.Budget
	if info.BudgetID == nil || *info.BudgetID != customerBudgetID(id) || b == nil || b.ResetAt == nil || b.Duration == nil {
		return CustomerUsage{}, fmt.Errorf("%w: the customer does not have the budget of Koodam", ErrCustomerNotFound)
	}
	period, ok := parseBudgetDuration(*b.Duration)
	if !ok {
		return CustomerUsage{}, errors.New("litellm: the budget period of the customer is not valid")
	}
	// The end of the filter is after the current time, so that a clock difference loses no call.
	q := url.Values{"end_user": {id}, "status_filter": {"success"}, "page": {"1"}, "page_size": {"1"},
		"start_date": {b.ResetAt.Add(-period).UTC().Format(spendLogTime)},
		"end_date":   {time.Now().Add(time.Hour).UTC().Format(spendLogTime)}}
	var logs struct {
		Total *int64 `json:"total"`
	}
	if err := l.do(ctx, http.MethodGet, "/spend/logs/v2?"+q.Encode(), nil, "", &logs); err != nil {
		return CustomerUsage{}, err
	}
	if logs.Total == nil || *logs.Total < 0 {
		return CustomerUsage{}, errors.New("litellm: the spend log answer has no total")
	}
	return CustomerUsage{Spend: info.Spend, Requests: *logs.Total, PeriodEnd: b.ResetAt.UTC()}, nil
}

// DeleteCustomer implements Customers. It deletes the customer first, because a budget with a
// customer stays in use.
func (l *LiteLLM) DeleteCustomer(ctx context.Context, id string) error {
	if id == "" {
		return errors.New("litellm: a customer ID is required")
	}
	err := l.do(ctx, http.MethodPost, "/customer/delete", map[string]any{"user_ids": []string{id}}, "", nil)
	if err != nil && !customerMissing(err) {
		return err
	}
	return l.do(ctx, http.MethodPost, "/budget/delete", map[string]string{"id": customerBudgetID(id)}, "", nil)
}
