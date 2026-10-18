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
	"errors"
	"time"
)

// ErrSpendNotSettled is the Usage error when the spend did not settle in the wait time.
// Usage also returns the count that it read, which can be low.
var ErrSpendNotSettled = errors.New("gateway: spend did not settle")

// ErrKeyNotFound is the Usage error when the gateway says that the key is not there.
var ErrKeyNotFound = errors.New("gateway: key not found")

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
	// BlockKey stops all later calls with the key. key is the key value or its HashKey.
	// A blocked key returns nil. A missing key returns nil only if the gateway says that the
	// key is missing.
	BlockKey(ctx context.Context, key string) error
	// Usage waits until the spend is stable and returns the count for the key. key is the key
	// value or its HashKey. If the spend does not settle, it returns the count and
	// ErrSpendNotSettled. If the key is missing, it returns ErrKeyNotFound.
	Usage(ctx context.Context, key string) (Usage, error)
}

// NewKeyValue returns a new random key value with the prefix "sk-kd-".
func NewKeyValue() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "sk-kd-" + hex.EncodeToString(b), nil
}
