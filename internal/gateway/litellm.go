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
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// LiteLLM is a Gateway for the LiteLLM proxy (tested with v1.104.2).
// The key value goes only in POST bodies. GET URLs use the SHA-256 hash of the key,
// which LiteLLM accepts, so the key is not in access logs.
type LiteLLM struct {
	BaseURL   string
	MasterKey string
	HTTP      *http.Client

	// Retries is the count of tries again after a connection error. HTTP errors are not retried.
	Retries    int
	RetryDelay time.Duration
	// SpendPoll, SpendSettle and SpendWait control the wait for a stable spend.
	// LiteLLM writes the spend in batches, so the first reads can be low or 0.
	// The spend is stable when it does not change for SpendSettle. SpendSettle must be
	// longer than proxy_batch_write_at on the gateway. SpendWait is the maximum wait.
	SpendPoll   time.Duration
	SpendSettle time.Duration
	SpendWait   time.Duration
}

// NewLiteLLM returns a client with the defaults from the stage 2 spike.
func NewLiteLLM(baseURL, masterKey string) *LiteLLM {
	return &LiteLLM{
		BaseURL: strings.TrimRight(baseURL, "/"), MasterKey: masterKey,
		// Do not follow redirects: a 307 or 308 sends the POST body with the key to the new URL.
		HTTP: &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}},
		Retries: 4, RetryDelay: 2 * time.Second,
		SpendPoll: 3 * time.Second, SpendSettle: 12 * time.Second, SpendWait: 40 * time.Second,
	}
}

// HTTPError is a response with a status other than 2xx. Body has the key value removed.
type HTTPError struct {
	Path   string
	Status int
	Body   string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("litellm %s: HTTP %d: %s", e.Path, e.Status, e.Body)
}

func hashKey(value string) string {
	h := sha256.Sum256([]byte(value))
	return hex.EncodeToString(h[:])
}

// do sends one request. secret and the master key are removed from any error text.
func (l *LiteLLM) do(ctx context.Context, method, path string, body any, secret string, out any) error {
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return err
		}
	}
	redact := func(s string) string {
		if secret != "" {
			s = strings.ReplaceAll(s, secret, "[key]")
		}
		if l.MasterKey != "" {
			s = strings.ReplaceAll(s, l.MasterKey, "[master key]")
		}
		return s
	}
	var lastErr error
	for try := 0; try <= l.Retries; try++ {
		if try > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(l.RetryDelay * time.Duration(try)):
			}
		}
		req, err := http.NewRequestWithContext(ctx, method, l.BaseURL+path, bytes.NewReader(payload))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+l.MasterKey)
		req.Header.Set("Content-Type", "application/json")
		resp, err := l.HTTP.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			lastErr = errors.New(redact(err.Error()))
			continue
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		_ = resp.Body.Close()
		if err != nil {
			lastErr = errors.New(redact(err.Error()))
			continue
		}
		if resp.StatusCode/100 != 2 {
			b := redact(string(data))
			if len(b) > 300 {
				b = b[:300]
			}
			return &HTTPError{Path: strings.SplitN(path, "?", 2)[0], Status: resp.StatusCode, Body: b}
		}
		if out != nil {
			if err := json.Unmarshal(data, out); err != nil {
				return fmt.Errorf("litellm %s: decode: %w", strings.SplitN(path, "?", 2)[0], err)
			}
		}
		return nil
	}
	return fmt.Errorf("litellm %s: %w", strings.SplitN(path, "?", 2)[0], lastErr)
}

func (l *LiteLLM) exists(ctx context.Context, value string) (bool, error) {
	err := l.do(ctx, http.MethodGet, "/key/info?key="+url.QueryEscape(hashKey(value)), nil, value, nil)
	if keyMissing(err) {
		return false, nil
	}
	return err == nil, err
}

// keyMissing is true only for a LiteLLM error about the key. A wrong route gives the
// FastAPI body {"detail":"Not Found"}, and a proxy can give HTML. Both are not proof
// that the key is missing, so a revoke fails closed.
func keyMissing(err error) bool {
	he, ok := errors.AsType[*HTTPError](err)
	if !ok || (he.Status != http.StatusNotFound && he.Status != http.StatusBadRequest) {
		return false
	}
	var body map[string]json.RawMessage
	if json.Unmarshal([]byte(he.Body), &body) != nil {
		return false
	}
	d, hasDetail := body["detail"]
	if hasDetail && string(d) == `"Not Found"` {
		return false
	}
	_, hasError := body["error"]
	return hasDetail || hasError
}

// CreateKey implements Gateway.
func (l *LiteLLM) CreateKey(ctx context.Context, s KeySpec) error {
	if s.Value == "" || s.Model == "" || s.TTL <= 0 {
		return errors.New("litellm: key value, model and TTL are required")
	}
	md := map[string]any{"koodam": true}
	for k, v := range s.Metadata {
		md[k] = v
	}
	body := map[string]any{
		"key": s.Value, "key_alias": s.Alias, "models": []string{s.Model},
		"max_budget": s.Budget, "duration": fmt.Sprintf("%ds", int64(s.TTL/time.Second)), "metadata": md,
	}
	err := l.do(ctx, http.MethodPost, "/key/generate", body, s.Value, nil)
	if err == nil {
		return nil
	}
	// A retry after a crash: the key can already exist.
	if _, ok := errors.AsType[*HTTPError](err); ok {
		if ok, e := l.exists(ctx, s.Value); e == nil && ok {
			return nil
		}
	}
	return err
}

// BlockKey implements Gateway.
func (l *LiteLLM) BlockKey(ctx context.Context, value string) error {
	err := l.do(ctx, http.MethodPost, "/key/block", map[string]string{"key": value}, value, nil)
	if keyMissing(err) {
		// Success only if /key/info also says that the key is not there.
		found, e := l.exists(ctx, value)
		if e != nil {
			return e
		}
		if found {
			return err
		}
		return nil
	}
	return err
}

type keyInfo struct {
	Info struct {
		Spend *float64 `json:"spend"`
	} `json:"info"`
}

type spendLog struct {
	Status           string  `json:"status"`
	PromptTokens     int64   `json:"prompt_tokens"`
	CompletionTokens int64   `json:"completion_tokens"`
	Spend            float64 `json:"spend"`
}

// Usage implements Gateway.
func (l *LiteLLM) Usage(ctx context.Context, value string) (Usage, error) {
	q := url.QueryEscape(hashKey(value))
	var last *float64
	// need is the count of equal reads after the first read that cover SpendSettle.
	need := max(1, int((l.SpendSettle+l.SpendPoll-1)/max(l.SpendPoll, 1)))
	stable, settled := 0, true
	deadline := time.Now().Add(l.SpendWait)
	for {
		var ki keyInfo
		if err := l.do(ctx, http.MethodGet, "/key/info?key="+q, nil, value, &ki); err != nil {
			return Usage{}, err
		}
		s := ki.Info.Spend
		if s != nil && last != nil && *s == *last {
			stable++
		} else {
			stable = 0
		}
		last = s
		if stable >= need {
			break
		}
		if time.Now().After(deadline) {
			settled = false
			break
		}
		select {
		case <-ctx.Done():
			return Usage{}, ctx.Err()
		case <-time.After(l.SpendPoll):
		}
	}
	var logs []spendLog
	if err := l.do(ctx, http.MethodGet, "/spend/logs?api_key="+q, nil, value, &logs); err != nil {
		return Usage{}, err
	}
	u := Usage{}
	if last != nil {
		u.Spend = *last
	}
	for _, x := range logs {
		if x.Status != "success" {
			u.FailedCalls++
			continue
		}
		u.Calls++
		u.InputTokens += x.PromptTokens
		u.OutputTokens += x.CompletionTokens
	}
	if !settled {
		return u, ErrSpendNotSettled
	}
	return u, nil
}
