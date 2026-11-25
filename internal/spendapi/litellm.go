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

// Package spendapi serves the Agent spend and budgets over HTTP.
package spendapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/mohanraj00/koodam/internal/gateway"
)

const maxGatewayReads = 16

var (
	errGateway    = errors.New("LiteLLM did not give valid spend data")
	errMissingKey = errors.New("LiteLLM did not find the key")
)

type liteLLM struct {
	*gateway.LiteLLM
	slots chan struct{}
}

func newLiteLLM(address, masterKey string) (*liteLLM, error) {
	u, err := url.Parse(address)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") ||
		u.User != nil || u.ForceQuery || u.RawQuery != "" || u.Fragment != "" || masterKey == "" {
		return nil, errors.New("a LiteLLM HTTP URL and a master key are necessary")
	}
	return &liteLLM{LiteLLM: gateway.NewLiteLLM(address, masterKey), slots: make(chan struct{}, maxGatewayReads)}, nil
}

// read sends one GET. Keep all gateway response text and transport errors private.
func (l *liteLLM) read(ctx context.Context, path string, query url.Values, out any) error {
	select {
	case l.slots <- struct{}{}:
		defer func() { <-l.slots }()
	case <-ctx.Done():
		return ctx.Err()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, l.BaseURL+path+"?"+query.Encode(), nil)
	if err != nil {
		return errGateway
	}
	req.Header.Set("Authorization", "Bearer "+l.MasterKey)
	resp, err := l.HTTP.Do(req)
	if err != nil {
		return errGateway
	}
	defer func() { _ = resp.Body.Close() }()

	const maxBody = 32 << 20
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil || len(data) > maxBody {
		return errGateway
	}
	if resp.StatusCode == http.StatusNotFound && path == "/key/info" && missingKey(data) {
		return errMissingKey
	}
	if resp.StatusCode != http.StatusOK || json.Unmarshal(data, out) != nil {
		return errGateway
	}
	return nil
}

// missingKey checks the LiteLLM error code so a bad route does not appear to be a missing key.
func missingKey(body []byte) bool {
	var data struct {
		Error struct {
			Type  string `json:"type"`
			Param string `json:"param"`
		} `json:"error"`
	}
	return json.Unmarshal(body, &data) == nil && data.Error.Type == "not_found_error" && data.Error.Param == "key"
}

func (l *liteLLM) lifetime(ctx context.Context, hash string) (*float64, error) {
	var data struct {
		Info struct {
			Spend *float64 `json:"spend"`
		} `json:"info"`
	}
	if err := l.read(ctx, "/key/info", url.Values{"key": {hash}}, &data); err != nil {
		return nil, err
	}
	if data.Info.Spend == nil || !finite(*data.Info.Spend) {
		return nil, errGateway
	}
	return data.Info.Spend, nil
}

func (l *liteLLM) period(ctx context.Context, hash string, from, end time.Time) (float64, error) {
	var days []struct {
		Day   string   `json:"startTime"`
		Spend *float64 `json:"spend"`
	}
	q := url.Values{
		"api_key": {hash}, "summarize": {"true"},
		"start_date": {from.Format(time.DateOnly)}, "end_date": {end.Format(time.DateOnly)},
	}
	if err := l.read(ctx, "/spend/logs", q, &days); err != nil {
		return 0, err
	}
	// LiteLLM includes midnight of the next day. Drop that day's summary.
	var spend float64
	for _, d := range days {
		day, err := time.Parse(time.DateOnly, d.Day)
		if err != nil || d.Spend == nil || !finite(*d.Spend) {
			return 0, errGateway
		}
		if !day.Before(from) && day.Before(end) {
			spend += *d.Spend
		}
	}
	if days == nil || !finite(spend) {
		return 0, errGateway
	}
	return spend, nil
}

func finite(n float64) bool { return !math.IsNaN(n) && !math.IsInf(n, 0) }

func keyHash(s string) (string, error) {
	if len(s) != 64 || strings.ContainsFunc(s, func(r rune) bool {
		return (r < '0' || r > '9') && (r < 'a' || r > 'f')
	}) {
		return "", errGateway
	}
	return s, nil
}
