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

package spendapi

import (
	"slices"
	"strings"
	"testing"
)

// The values in this file are synthetic. None is a real credential.
func TestEachRedactionPattern(t *testing.T) {
	cases := map[string]struct{ line, secret, want string }{
		"private key": {"-----BEGIN RSA PRIVATE KEY-----MIIBOgIBAAJB", "MIIBOgIBAAJB", redacted},
		"JSON Web Token": {"token eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJ1c2VyIn0.c2lnbmF0dXJl ok",
			"eyJzdWIiOiJ1c2VyIn0", "token [redacted] ok"},
		"sk- key": {"key sk-kd-0123456789abcdef0123456789abcdef0123456789abcdef used",
			"0123456789abcdef", "key [redacted] used"},
		"GitHub token":   {"clone with ghp_ABCDEFGHIJKLMNOPQRSTuvwxyz0123", "ghp_ABCDEFGHIJ", "clone with [redacted]"},
		"AWS access key": {"id AKIAABCDEFGHIJKLMNOP end", "AKIAABCDEFGHIJKLMNOP", "id [redacted] end"},
		"Slack token":    {"post xoxb-1234567890-abcdefghij", "xoxb-1234567890", "post [redacted]"},
		"SHA-256 hex": {"hash " + strings.Repeat("ab12", 16) + " stored", strings.Repeat("ab12", 16),
			"hash [redacted] stored"},
		"authorization": {"curl -H Bearer Zm9vYmFyYmF6cXV4", "Zm9vYmFyYmF6cXV4", "curl -H Bearer [redacted]"},
		"URL login": {"git clone https://alice:hunter2pass@git.example.com/repo", "hunter2pass",
			"git clone https://[redacted]git.example.com/repo"},
		"credential field": {`{"db_password": "a b c", "login":'alice'} GITHUB_TOKEN=abcdef`, "alice",
			`{"db_password": [redacted], "login":[redacted]} GITHUB_TOKEN=[redacted]`},
	}
	for _, r := range redactions {
		c, ok := cases[r.name]
		if !ok {
			t.Fatalf("the pattern %q has no test", r.name)
		}
		if !r.pattern.MatchString(c.line) {
			t.Errorf("the pattern %q does not match its test line", r.name)
		}
		got := redactLine(c.line)
		if strings.Contains(got, c.secret) || got != c.want {
			t.Errorf("%s: redactLine = %q, want %q", r.name, got, c.want)
		}
	}
	if len(cases) != len(redactions) {
		t.Fatal("a test case has no pattern")
	}
}

func TestRedactionOfQuotedValues(t *testing.T) {
	for line, want := range map[string]string{
		`{"password":"prefix\"remaining-secret"}`: `{"password":[redacted]}`,
		`token='it\'s-secret' next`:               `token=[redacted] next`,
		`password="no end quote here`:             `password=[redacted]`,
		`password="ends in a backslash\`:          `password=[redacted]`,
	} {
		if got := redactLine(line); got != want {
			t.Errorf("redactLine(%q) = %q, want %q", line, got, want)
		}
	}
}

func TestRedactionKeepsOrdinaryText(t *testing.T) {
	for _, line := range []string{
		`{"prompt_tokens": 1200, "completion_tokens": 30}`,
		"Login succeeded for the run.",
		"commit 0123456789abcdef0123456789abcdef01234567",
		"the secrets of the run are mounted",
	} {
		if got := redactLine(line); got != line {
			t.Errorf("redactLine(%q) = %q, want no change", line, got)
		}
	}
}

func TestRedactionRemovesControlSequences(t *testing.T) {
	got := redactLine("\x1b[31mkey\x1b[0m=sk-kd-0123456789abcdef\r\x00 \xff")
	if got != "key=[redacted] �" {
		t.Fatalf("redactLine = %q", got)
	}
}

func TestRedactionOfAPEMBlock(t *testing.T) {
	lines := []string{"before", "-----BEGIN PRIVATE KEY-----", "MIIEvQIBADANBgkqhkiG9w0BAQEFAASC", "-----END PRIVATE KEY-----", "after"}
	got := redactLines(slices.Clone(lines))
	want := []string{"before", redacted, redacted, redacted, "after"}
	if !slices.Equal(got, want) {
		t.Fatalf("redactLines = %q, want %q", got, want)
	}
}
