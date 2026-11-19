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

package utils

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
)

// The file names of the release samples. docs/egress-spans.md names them.
const (
	SpanSample    = "egress-spans.jsonl"
	GatewaySample = "gateway-log.jsonl"
	// AppSpanSample is the sample of the App ingress spans. docs/app-ingress-spans.md is its
	// contract.
	AppSpanSample = "app-ingress-spans.jsonl"
)

// SamplesEnv is the env variable with the folder of the release samples. If it is empty, the
// e2e tests write no sample.
const SamplesEnv = "KOODAM_E2E_SAMPLES"

// keyWord matches a gateway key: the master key and the keys of the runs start with "sk-".
var keyWord = regexp.MustCompile(`sk-[A-Za-z0-9_-]{8,}`)

// signedToken matches a JSON Web Token: a base64url JSON header, a payload and a signature.
var signedToken = regexp.MustCompile(`eyJ[A-Za-z0-9_-]{4,}\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]*`)

// CheckSample returns an error if data holds a credential: one of the secrets, a word with the
// key prefix "sk-", a bearer token or a signed token (JWT). The error names the file and the kind of the match. It
// never holds the value.
func CheckSample(name string, data []byte, secrets ...string) error {
	for i, s := range secrets {
		if s != "" && bytes.Contains(data, []byte(s)) {
			return fmt.Errorf("the sample %s holds secret number %d", name, i+1)
		}
	}
	if keyWord.Match(data) {
		return fmt.Errorf("the sample %s holds a word with the key prefix sk-", name)
	}
	if bytes.Contains(bytes.ToLower(data), []byte("bearer ")) {
		return fmt.Errorf("the sample %s holds a bearer token", name)
	}
	if signedToken.Match(data) {
		return fmt.Errorf("the sample %s holds a signed token", name)
	}
	return nil
}

// WriteSamples checks each sample, then writes each one to dir. A sample is 1 JSON object on
// each line. If a check fails, WriteSamples writes no file.
func WriteSamples(dir string, samples map[string][]byte, secrets ...string) error {
	names := slices.Sorted(maps.Keys(samples))
	for _, name := range names {
		data := samples[name]
		if len(bytes.TrimSpace(data)) == 0 {
			return fmt.Errorf("the sample %s is empty", name)
		}
		if err := CheckSample(name, data, secrets...); err != nil {
			return err
		}
		for i, line := range bytes.Split(bytes.TrimRight(data, "\n"), []byte("\n")) {
			if !json.Valid(line) || !bytes.HasPrefix(line, []byte("{")) {
				return fmt.Errorf("line %d of the sample %s is not a JSON object", i+1, name)
			}
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, name := range names {
		data := append(bytes.TrimRight(samples[name], "\n"), '\n')
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// JSONLines gives each item of a JSON list as 1 compact line. The gateway gives its spend logs
// as a list, and the sample has 1 log on each line.
func JSONLines(list []byte) ([]byte, error) {
	var items []json.RawMessage
	if err := json.Unmarshal(list, &items); err != nil {
		return nil, fmt.Errorf("the gateway logs are not a JSON list: %w", err)
	}
	var out bytes.Buffer
	for _, item := range items {
		if err := json.Compact(&out, item); err != nil {
			return nil, err
		}
		out.WriteByte('\n')
	}
	return out.Bytes(), nil
}
