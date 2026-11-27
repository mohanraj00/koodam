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
	"regexp"
	"strings"
)

// redacted replaces each credential value in a response.
const redacted = "[redacted]"

// redaction is one fixed pattern. The value of group 1, if the pattern has it, stays in the text,
// so that a reader can see the name of the field.
type redaction struct {
	name    string
	pattern *regexp.Regexp
}

// control matches a terminal escape sequence or a control character other than a tab. The server
// removes these before the patterns, so that a sequence cannot split a credential from its name.
var control = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b[@-_]|[\x00-\x08\x0b-\x1f\x7f]`)

// pemBegin and pemEnd match the first line and the last line of a PEM private key.
var (
	pemBegin = regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----`)
	pemEnd   = regexp.MustCompile(`-----END [A-Z0-9 ]*PRIVATE KEY-----`)
)

// redactions is the fixed list of the credential patterns, in the order of use. Each pattern has a
// test in redact_test.go. Add a pattern only with a test. The patterns can redact more text than a
// credential. That is the safe failure.
var redactions = []redaction{
	// A PEM private key line. redactLines also redacts each line up to the END line.
	{"private key", regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*`)},
	// A JSON Web Token, for example a Kubernetes service account token or an OIDC ID token.
	{"JSON Web Token", regexp.MustCompile(`eyJ[A-Za-z0-9_-]{4,}\.[A-Za-z0-9_-]{4,}\.[A-Za-z0-9_-]*`)},
	// A gateway key, an OpenAI key or an Anthropic key. Each starts with sk-.
	{"sk- key", regexp.MustCompile(`sk-[A-Za-z0-9_-]{8,}`)},
	// A GitHub token.
	{"GitHub token", regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}`)},
	// An AWS access key ID.
	{"AWS access key", regexp.MustCompile(`(?:AKIA|ASIA)[0-9A-Z]{16}`)},
	// A Slack token.
	{"Slack token", regexp.MustCompile(`xox[abprs]-[A-Za-z0-9-]{10,}`)},
	// A SHA-256 value in hex, for example the hash of a gateway key.
	{"SHA-256 hex", regexp.MustCompile(`[0-9a-fA-F]{64}`)},
	// The credentials of an Authorization header.
	{"authorization", regexp.MustCompile(`(?i)(\b(?:bearer|basic|token)\s+)[A-Za-z0-9._~+/=-]{8,}`)},
	// The login and the password of a URL.
	{"URL login", regexp.MustCompile(`(://)[^/\s:@]+:[^/\s@]+@`)},
	// The value of a field with a credential name, in key=value, key: value or JSON form. A name
	// such as prompt_tokens does not match, because the name must end after the credential word. A
	// quoted value includes each escaped character, and a value with no end quote goes to the end.
	{"credential field", regexp.MustCompile(`(?i)(\b[A-Za-z0-9_-]*?(?:password|passwd|pwd|secret|token|` +
		`api[_-]?key|access[_-]?key|private[_-]?key|master[_-]?key|login|credentials?|authorization)\b` +
		`["']?\s*[:=]\s*)(?:"(?:[^"\\]|\\.?)*"?|'(?:[^'\\]|\\.?)*'?|[^\s"',;]+)`)},
}

// cleanLine makes a line valid UTF-8 and removes the control characters. Each check of a line uses
// the clean line, so that a control sequence cannot hide a credential or a PEM boundary.
func cleanLine(line string) string {
	return control.ReplaceAllLiteralString(strings.ToValidUTF8(line, "\uFFFD"), "")
}

// redactLine removes the control characters, and then replaces each match of the patterns.
func redactLine(line string) string {
	return replaceMatches(cleanLine(line))
}

func replaceMatches(line string) string {
	for _, r := range redactions {
		if r.pattern.NumSubexp() == 0 {
			line = r.pattern.ReplaceAllLiteralString(line, redacted)
		} else {
			line = r.pattern.ReplaceAllString(line, "${1}"+redacted)
		}
	}
	return line
}

// redactText redacts a text that can have more than one line, for example a condition message.
func redactText(text string) string {
	return strings.Join(redactLines(strings.Split(text, "\n")), "\n")
}

// redactLines replaces each match of the patterns in each line. A PEM private key has more than one
// line, so each line after a BEGIN line, up to the END line, becomes the redacted mark. A log tail
// can start in a key. Thus an END line with no BEGIN line before it makes each line before it, and
// the END line, the redacted mark.
func redactLines(lines []string) []string {
	inKey := false
	for i, line := range lines {
		line = cleanLine(line)
		switch {
		case inKey:
			inKey = !strings.Contains(line, "-----END")
			lines[i] = redacted
		case pemEnd.MatchString(line) && !pemBegin.MatchString(line):
			for j := range i + 1 {
				lines[j] = redacted
			}
		default:
			inKey = pemBegin.MatchString(line) && !strings.Contains(line, "-----END")
			lines[i] = replaceMatches(line)
		}
	}
	return lines
}
