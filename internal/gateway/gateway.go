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

// Package gateway issues and revokes model keys for agent runs, and reads their usage.
package gateway

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"
)

// KeySpec is the key for one run. The caller makes Value with NewKeyValue and saves it
// before CreateKey, so that a retry after a crash uses the same key.
type KeySpec struct {
	Value    string
	Alias    string
	Model    string
	Budget   float64
	TTL      time.Duration
	Metadata map[string]string
}

// Usage is the gateway count for one key. The counts come from successful calls only.
type Usage struct {
	Spend        float64
	Calls        int64
	FailedCalls  int64
	InputTokens  int64
	OutputTokens int64
}

// Gateway is a model gateway with keys for each run.
type Gateway interface {
	// CreateKey makes the key. If the key already exists, CreateKey returns nil.
	CreateKey(ctx context.Context, spec KeySpec) error
	// BlockKey stops all later calls with the key. A blocked or missing key returns nil.
	BlockKey(ctx context.Context, value string) error
	// Usage waits until the spend is stable and returns the count for the key.
	Usage(ctx context.Context, value string) (Usage, error)
}

// NewKeyValue returns a new random key value with the prefix "sk-kd-".
func NewKeyValue() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "sk-kd-" + hex.EncodeToString(b), nil
}
