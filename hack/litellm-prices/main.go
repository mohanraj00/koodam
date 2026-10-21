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

// Command litellm-prices makes the price subset in internal/usage/prices from the LiteLLM file
// model_prices_and_context_window.json and the LiteLLM LICENSE file. Run it with make prices.
// It keeps only the chat models of the providers anthropic and openai, and only the 4 token
// prices. It puts the licence notice into the subset, so the notice goes with the binary.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
)

// price is 1 model of the subset. The field names are the LiteLLM names.
type price struct {
	Input      *float64 `json:"input_cost_per_token,omitempty"`
	Output     *float64 `json:"output_cost_per_token,omitempty"`
	CacheRead  *float64 `json:"cache_read_input_token_cost,omitempty"`
	CacheWrite *float64 `json:"cache_creation_input_token_cost,omitempty"`
}

type entry struct {
	price
	Provider string `json:"litellm_provider"`
	Mode     string `json:"mode"`
}

// subset is the file that koodam embeds.
type subset struct {
	Source  string           `json:"source"`
	Commit  string           `json:"commit"`
	License string           `json:"license"`
	Models  map[string]price `json:"models"`
}

var commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

func main() {
	commit := flag.String("commit", "", "the LiteLLM commit of the input files (40 hex characters)")
	license := flag.String("license", "", "the LiteLLM LICENSE file at the same commit")
	flag.Parse()
	if flag.NArg() != 1 || *license == "" {
		fmt.Fprintln(os.Stderr, "usage: litellm-prices -commit SHA -license LICENSE model_prices_and_context_window.json")
		os.Exit(2)
	}
	if err := run(*commit, flag.Arg(0), *license, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(commit, path, licensePath string, out io.Writer) error {
	if !commitPattern.MatchString(commit) {
		return fmt.Errorf("the commit must be 40 hex characters")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	license, err := os.ReadFile(licensePath)
	if err != nil {
		return err
	}
	if !strings.Contains(string(license), "MIT License") {
		return fmt.Errorf("%s is not the MIT licence of LiteLLM", licensePath)
	}
	var all map[string]json.RawMessage
	if err := json.Unmarshal(data, &all); err != nil {
		return err
	}
	s := subset{
		Source:  "https://github.com/BerriAI/litellm/blob/" + commit + "/model_prices_and_context_window.json",
		Commit:  commit,
		License: string(license),
		Models:  models(all),
	}
	if len(s.Models) == 0 {
		return fmt.Errorf("%s has no anthropic or openai chat model", path)
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", " ")
	return enc.Encode(s)
}

// models keeps the chat and responses models of anthropic and openai that have an input and an
// output price. It skips a name with a "/", because the harnesses do not send it.
func models(all map[string]json.RawMessage) map[string]price {
	out := map[string]price{}
	for name, raw := range all {
		var e entry
		if strings.Contains(name, "/") || json.Unmarshal(raw, &e) != nil {
			continue
		}
		if e.Provider != "anthropic" && e.Provider != "openai" {
			continue
		}
		if (e.Mode != "chat" && e.Mode != "responses") || e.Input == nil || e.Output == nil {
			continue
		}
		out[name] = e.price
	}
	return out
}
