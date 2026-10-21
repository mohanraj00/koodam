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

package usage

import (
	"math"
	"strings"
	"testing"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-12 }

func TestCostMultipliesEachType(t *testing.T) {
	in, out, read, write := 3e-6, 15e-6, 3e-7, 3.75e-6
	tab := &Table{Models: map[string]Price{
		"m":       {Input: &in, Output: &out, CacheRead: &read, CacheWrite: &write},
		"nocache": {Input: &in, Output: &out},
	}}
	r := Row{Model: "m", Input: 1000, Output: 100, CacheRead: 10000, CacheWrite: 2000}
	if got, ok := tab.Cost(r); !ok || !near(got, 1000*in+100*out+10000*read+2000*write) {
		t.Errorf("cost: got %v %v", got, ok)
	}
	want := 1000*in + 100*out + 10000*read + 2000*write
	for _, snapshot := range []string{"m-20250929", "m-2024-08-06", "m-0613"} {
		r.Model = snapshot
		if got, ok := tab.Cost(r); !ok || !near(got, want) {
			t.Errorf("%s must use the price of m: got %v %v", snapshot, got, ok)
		}
	}
	r.Model = "m-12345"
	if _, ok := tab.Cost(r); ok {
		t.Error("a suffix of 5 digits is not a snapshot suffix")
	}
	r.Model = "nocache"
	if got, ok := tab.Cost(r); !ok || !near(got, (1000+10000+2000)*in+100*out) {
		t.Errorf("a missing cache price must use the input price: got %v %v", got, ok)
	}
	r.Model = "unknown-model"
	if got, ok := tab.Cost(r); ok || got != 0 {
		t.Errorf("an unknown model must have no price: got %v %v", got, ok)
	}
}

func TestEmbeddedPricesHaveTheHarnessModels(t *testing.T) {
	tab, err := Prices()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tab.License, "MIT License") || !strings.Contains(tab.License, "Copyright (c) 2023 Berri AI") ||
		!strings.Contains(tab.License, "Permission is hereby granted") {
		t.Error("the embedded table must have the LiteLLM licence notice")
	}
	if len(tab.Commit) != 40 || !strings.Contains(tab.Source, tab.Commit) {
		t.Errorf("source %q, commit %q", tab.Source, tab.Commit)
	}
	for _, m := range []string{"claude-sonnet-4-5-20250929", "claude-haiku-4-5", "gpt-5-mini"} {
		if _, ok := tab.Cost(Row{Model: m, Input: 1}); !ok {
			t.Errorf("the table has no price for %s", m)
		}
	}
}
