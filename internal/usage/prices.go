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
	_ "embed"
	"encoding/json"
	"fmt"
	"regexp"
	"sync"
)

// Harness names in a Row.
const (
	ClaudeCode = "claude-code"
	Codex      = "codex"
)

// Row is the sum of the tokens of 1 model of 1 harness. Input does not include the cache reads.
type Row struct {
	Harness    string
	Model      string
	Input      int64
	Output     int64
	CacheRead  int64
	CacheWrite int64
}

// Price is the price of 1 token of each type, in USD. The JSON names are the LiteLLM names.
type Price struct {
	Input      *float64 `json:"input_cost_per_token,omitempty"`
	Output     *float64 `json:"output_cost_per_token,omitempty"`
	CacheRead  *float64 `json:"cache_read_input_token_cost,omitempty"`
	CacheWrite *float64 `json:"cache_creation_input_token_cost,omitempty"`
}

// Table is the price subset. License is the licence notice of LiteLLM (MIT). It is in the
// embedded file, so the notice goes with the binary.
type Table struct {
	Source  string           `json:"source"`
	Commit  string           `json:"commit"`
	License string           `json:"license"`
	Models  map[string]Price `json:"models"`
}

//go:embed prices/litellm.json
var litellm []byte

// Prices gives the embedded LiteLLM price subset. It reads the embedded file 1 time only.
// Do not change the table that it gives.
var Prices = sync.OnceValues(func() (*Table, error) {
	t := &Table{}
	if err := json.Unmarshal(litellm, t); err != nil {
		return nil, fmt.Errorf("read the embedded price table: %w", err)
	}
	return t, nil
})

// snapshotSuffix is the date or the version at the end of a model snapshot name, for example
// -20250929 (Anthropic), -2024-08-06 or -0613 (OpenAI).
var snapshotSuffix = regexp.MustCompile(`-([0-9]{8}|[0-9]{4}-[0-9]{2}-[0-9]{2}|[0-9]{4})$`)

// Cost gives the projected cost of r in USD. If the table has no price for the snapshot name of
// the model, Cost uses the name with no snapshot suffix. It is false if the table has no input
// and output price for the model. If the table has no cache price, the input price is used.
// The cache write price is the 5-minute price, and the price tiers above 200k tokens are not used.
func (t *Table) Cost(r Row) (float64, bool) {
	p, ok := t.Models[r.Model]
	if !ok {
		p, ok = t.Models[snapshotSuffix.ReplaceAllString(r.Model, "")]
	}
	if !ok || p.Input == nil || p.Output == nil {
		return 0, false
	}
	cacheRead, cacheWrite := *p.Input, *p.Input
	if p.CacheRead != nil {
		cacheRead = *p.CacheRead
	}
	if p.CacheWrite != nil {
		cacheWrite = *p.CacheWrite
	}
	return float64(r.Input)**p.Input + float64(r.Output)**p.Output +
		float64(r.CacheRead)*cacheRead + float64(r.CacheWrite)*cacheWrite, true
}
