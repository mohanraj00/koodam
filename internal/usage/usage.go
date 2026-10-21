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
	"bufio"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// remoteClaude is the tag of a Claude Code line in the remote output. Codex lines have the tag
// Codex.
const remoteClaude = "claude"

// RemoteScript is the script that koodam usage sends to "sh -s" in the Workspace pod. It is a
// fixed string: no user input goes into it. It needs only find, cat, grep, sed and tail. If HOME
// is empty, it prints nothing.
//
// Claude Code: the script keeps the assistant lines with a usage object. It removes the text
// from "content" to the last "usage", so the message text and the tool input are not printed.
// The removal fails closed: if a line has "content" and the removal does not match, or if a
// "usage" comes before the first "content", the script prints nothing for the line.
// Codex: for each session file, it keeps the last model of the turn context and the last
// total_token_usage object, which holds only numbers.
//
// grep -o prints only a message id, a model name or a token count. A quote in the text of a
// JSON line is escaped (\"), so the text cannot match these patterns. For the same reason, a
// grep -F match of a quoted key cannot match a message text.
const RemoteScript = `set -u
h="${HOME:-}"
[ -n "$h" ] || exit 0
c="$h/.claude/projects"
x="$h/.codex/sessions"
if [ -d "$c" ]; then
  find "$c" -type f -name '*.jsonl' -exec cat {} + 2>/dev/null |
    grep -F '"type":"assistant"' | grep -F '"usage":{' |
    sed -n '
/"usage":{.*"content":/d
/"content":/{
s/"content":\[.*"usage":{/"usage":{/
t keep
d
}
:keep
s/^/"koodam_line":1,/p
' |
    grep -o -E '"koodam_line":1|"id":"msg_[A-Za-z0-9_]{1,64}"|"model":"[A-Za-z0-9._:@-]{1,100}"|"(input|output|cache_read_input|cache_creation_input)_tokens":[0-9]{1,15}' |
    sed 's/^/` + remoteClaude + ` /'
fi
if [ -d "$x" ]; then
  find "$x" -type f -name '*.jsonl' 2>/dev/null | while IFS= read -r f; do
    echo '` + Codex + ` "koodam_line":1'
    # JSON escapes each quote in a string, so this match and the next one cannot match a text.
    grep -F '"type":"turn_context"' "$f" | grep -o -E '"model":"[A-Za-z0-9._:@-]{1,100}"' |
      tail -n 1 | sed 's/^/` + Codex + ` /'
    grep -F '"total_token_usage":{' "$f" | tail -n 1 |
      sed -n 's/.*"total_token_usage":{\([^}]*\)}.*/\1/p' | grep -o -E '"[a-z_]{1,40}":[0-9]{1,15}' |
      sed 's/^/` + Codex + ` /'
  done
fi
exit 0
`

// tokens is 1 record of the remote output: 1 Claude Code message or 1 Codex session file.
type tokens struct {
	id, model string
	n         map[string]int64
}

var item = regexp.MustCompile(`^"([a-z_]{1,40})":("[A-Za-z0-9._:@-]{1,100}"|[0-9]{1,15})$`)

// Parse reads the output of RemoteScript and gives the sums by harness and by model, sorted.
// A Claude Code message counts 1 time, also if it is on more than 1 line. A line that is not
// in the output format of RemoteScript is an error, and the error does not contain the line.
func Parse(r io.Reader) ([]Row, error) {
	var claude, codex []*tokens
	var cur *tokens
	sc := bufio.NewScanner(r)
	for n := 1; sc.Scan(); n++ {
		harness, field, ok := strings.Cut(sc.Text(), " ")
		m := item.FindStringSubmatch(field)
		if !ok || m == nil || (harness != remoteClaude && harness != Codex) {
			return nil, fmt.Errorf("line %d of the remote output is not in the usage format", n)
		}
		key, value := m[1], strings.Trim(m[2], `"`)
		if key == "koodam_line" {
			cur = &tokens{n: map[string]int64{}}
			if harness == Codex {
				codex = append(codex, cur)
			} else {
				claude = append(claude, cur)
			}
			continue
		}
		if cur == nil {
			return nil, fmt.Errorf("line %d of the remote output has no record start", n)
		}
		switch {
		case key == "id":
			cur.id = value
		case key == "model" && (cur.model == "" || harness == Codex):
			cur.model = value
		case key != "model":
			v, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("line %d of the remote output has a bad count", n)
			}
			cur.n[key] = v
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return sum(claude, codex), nil
}

func sum(claude, codex []*tokens) []Row {
	rows := map[[2]string]*Row{}
	add := func(harness, model string) *Row {
		if model == "" {
			model = "unknown"
		}
		k := [2]string{harness, model}
		if rows[k] == nil {
			rows[k] = &Row{Harness: harness, Model: model}
		}
		return rows[k]
	}
	// Claude Code writes 1 line for each content block of a message, with the same id. Use the
	// largest count of each type for the id.
	byID := map[string]*tokens{}
	for _, t := range claude {
		if t.id == "" {
			continue
		}
		if byID[t.id] == nil {
			byID[t.id] = &tokens{n: map[string]int64{}}
		}
		if byID[t.id].model == "" {
			// The first line of a message can have no model, and a later line can have it.
			byID[t.id].model = t.model
		}
		for k, v := range t.n {
			byID[t.id].n[k] = max(byID[t.id].n[k], v)
		}
	}
	for _, t := range byID {
		if t.n["input_tokens"]+t.n["output_tokens"]+t.n["cache_read_input_tokens"]+
			t.n["cache_creation_input_tokens"] == 0 {
			// For example a local message of Claude Code with the model <synthetic>.
			continue
		}
		r := add(ClaudeCode, t.model)
		r.Input += t.n["input_tokens"]
		r.Output += t.n["output_tokens"]
		r.CacheRead += t.n["cache_read_input_tokens"]
		r.CacheWrite += t.n["cache_creation_input_tokens"]
	}
	// For Codex, input_tokens includes cached_input_tokens, and output_tokens includes the
	// reasoning tokens.
	for _, t := range codex {
		if len(t.n) == 0 {
			continue
		}
		r := add(Codex, t.model)
		cached := min(t.n["cached_input_tokens"], t.n["input_tokens"])
		r.Input += t.n["input_tokens"] - cached
		r.CacheRead += cached
		r.Output += t.n["output_tokens"]
	}
	out := make([]Row, 0, len(rows))
	for _, r := range rows {
		out = append(out, *r)
	}
	slices.SortFunc(out, func(a, b Row) int {
		return strings.Compare(a.Harness+" "+a.Model, b.Harness+" "+b.Model)
	})
	return out
}
