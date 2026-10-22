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

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// codexInputIsTerminal lets a test check the terminal refusal without a real login.
var codexInputIsTerminal = func(in io.Reader) bool {
	file, ok := in.(*os.File)
	return ok && term.IsTerminal(int(file.Fd())) //nolint:gosec // A file descriptor fits in an int.
}

func runCodexLogin(cmd *cobra.Command, conn connect, stdin, remove bool) error {
	var data []byte
	if !remove {
		if !stdin || codexInputIsTerminal(cmd.InOrStdin()) {
			return errors.New("use a pipe or a redirect: koodam login codex --stdin does not read from a terminal")
		}
		var err error
		data, err = readCodexLogin(cmd.InOrStdin())
		if err != nil {
			return err
		}
	}
	e, err := conn()
	if err != nil {
		return err
	}
	if remove {
		return e.removeLogin(cmd, harnessCodex)
	}
	verb, err := e.storeLogin(cmd, harnessCodex, data)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "secret/%s %s in namespace %s: key %s\n",
		loginName, verb, e.namespace, harnessCodex)
	return nil
}

// readCodexLogin checks the file format. It does not check the login with the provider.
// An error contains no input and no JSON parser error.
func readCodexLogin(in io.Reader) ([]byte, error) {
	const limit = 256 * 1024
	data, err := io.ReadAll(io.LimitReader(in, limit+1))
	if err != nil || len(data) > limit {
		return nil, errors.New("read the Codex login: the read failed or the input is too large")
	}
	var login struct {
		Mode   string `json:"auth_mode"`
		APIKey string `json:"OPENAI_API_KEY"`
		Tokens struct {
			ID      string `json:"id_token"`
			Access  string `json:"access_token"`
			Refresh string `json:"refresh_token"`
		} `json:"tokens"`
		LastRefresh *time.Time `json:"last_refresh"`
	}
	if json.Unmarshal(data, &login) != nil || (login.Mode != "" && login.Mode != "chatgpt") ||
		login.APIKey != "" || strings.TrimSpace(login.Tokens.ID) == "" ||
		strings.TrimSpace(login.Tokens.Access) == "" || strings.TrimSpace(login.Tokens.Refresh) == "" {
		return nil, errors.New("the Codex login must be ChatGPT login JSON with id_token, access_token and refresh_token; " +
			"API keys are not permitted")
	}
	return data, nil
}

func revokeNoteFor(key string) string {
	if key == harnessCodex {
		return "The login can still be valid. Revoke it in your OpenAI account settings first. " +
			"Then remove the local login in each Workspace."
	}
	return revokeNote
}
