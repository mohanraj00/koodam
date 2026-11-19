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
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	span = `{"v":2,"kind":"llm","host":"gateway","result":"ok","bytesIn":10,"bytesOut":20}`
	// appSpan is a span of docs/app-ingress-spans.md. The caller and the tenant are labels.
	appSpan = `{"v":1,"kind":"app-ingress","method":"GET","status":200,"outcome":"ok","caller":"c1"}`
	log     = `{"request_id":"r1","api_key":"0a1b2c3d","metadata":{"user_api_key_alias":"kd-1"},"spend":0.1}`
)

func TestCheckSampleRefusesCredentials(t *testing.T) {
	const master = "sk-master-value-1234"
	// jwt has the shape of a signed token: {"alg":"ES256"}, a payload and a signature.
	const jwt = "eyJhbGciOiJFUzI1NiJ9.eyJzdWIiOiJ4In0.c2ln"
	for _, c := range []struct{ name, data, want string }{
		{"secret", `{"note":"x` + "0123456789abcdef" + `"}`, "secret number 2"},
		{"key prefix", `{"key":"sk-kd-AbCdEf0123456789"}`, "key prefix sk-"},
		{"master key", `{"key":"` + master + `"}`, "secret number 1"},
		{"bearer", `{"headers":{"authorization":"Bearer abc"}}`, "bearer token"},
		{"signed token", `{"claim":"` + jwt + `"}`, "signed token"},
	} {
		err := CheckSample(c.name, []byte(c.data), master, "0123456789abcdef")
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, want an error with %q", c.name, err, c.want)
			continue
		}
		// The error must not hold the value of the credential.
		for _, v := range []string{master, "0123456789abcdef", "sk-kd-AbCdEf0123456789", "Bearer abc", jwt} {
			if strings.Contains(err.Error(), v) {
				t.Errorf("%s: the error holds the credential value", c.name)
			}
		}
	}
	if err := CheckSample("clean", []byte(span+"\n"+log+"\n"+appSpan), master); err != nil {
		t.Errorf("a clean sample: %v", err)
	}
}

func TestWriteSamples(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "samples")
	if err := WriteSamples(dir, map[string][]byte{SpanSample: []byte(span), GatewaySample: []byte(log + "\n")},
		"sk-master"); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{SpanSample: span + "\n", GatewaySample: log + "\n"} {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(got) != want {
			t.Errorf("%s: got %q, %v", name, got, err)
		}
	}
}

func TestWriteSamplesWritesNoFileAfterAFailedCheck(t *testing.T) {
	for _, c := range []struct{ name, data string }{
		{"empty", " \n"},
		{"not JSON", "span\n"},
		{"not an object", "[1]\n"},
		{"credential", `{"key":"sk-kd-AbCdEf0123456789"}`},
	} {
		dir := filepath.Join(t.TempDir(), "samples")
		err := WriteSamples(dir, map[string][]byte{SpanSample: []byte(span), GatewaySample: []byte(c.data)})
		if err == nil {
			t.Errorf("%s: no error", c.name)
		}
		if _, statErr := os.Stat(dir); !os.IsNotExist(statErr) {
			t.Errorf("%s: the folder exists after a failed check", c.name)
		}
	}
}

func TestJSONLines(t *testing.T) {
	got, err := JSONLines([]byte("[\n " + log + ",\n {\"request_id\": \"r2\"}\n]"))
	if err != nil {
		t.Fatal(err)
	}
	if want := log + "\n" + `{"request_id":"r2"}` + "\n"; string(got) != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if _, err := JSONLines([]byte(`{"data":[]}`)); err == nil {
		t.Error("an object is not a list, but JSONLines gave no error")
	}
}
