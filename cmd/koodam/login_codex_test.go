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
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"testing/iotest"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// fakeCodexLogin makes synthetic data. No saved login is used.
func fakeCodexLogin(t *testing.T) (string, string) {
	t.Helper()
	token := rand.Text()
	data, err := json.Marshal(map[string]any{"auth_mode": "chatgpt", "OPENAI_API_KEY": nil,
		"tokens":       map[string]string{"id_token": token, "access_token": token, "refresh_token": token},
		"last_refresh": "2026-10-22T00:00:00Z"})
	if err != nil {
		t.Fatal("cannot make synthetic login data")
	}
	return string(data), token
}

func TestCodexLoginStoreUpdateAndRemove(t *testing.T) {
	var data, token string
	kube := newKube(t, nil, loginSecret(map[string]string{managedByLabel: managedBy},
		map[string][]byte{harnessClaude: []byte(otherValue)}))
	for range 2 {
		data, token = fakeCodexLogin(t)
		out, err := runCodexTest(t, kube, data, token, "--stdin")
		if err != nil {
			t.Fatal("cannot store the synthetic login")
		}
		wantAll(t, out, "key codex", "updated")
		s, err := getLogin(t, kube)
		if err != nil || string(s.Data[harnessCodex]) != data || string(s.Data[harnessClaude]) != otherValue {
			t.Fatal("the Secret does not keep both harness logins")
		}
	}
	out, err := runCodexTest(t, kube, "", token, "--remove")
	if err != nil {
		t.Fatal("cannot remove the Codex login")
	}
	wantAll(t, out, "key codex removed", "OpenAI account", "each Workspace")
	s, err := getLogin(t, kube)
	if err != nil || len(s.Data) != 1 || string(s.Data[harnessClaude]) != otherValue {
		t.Fatal("remove changed the other login")
	}
}

func TestCodexLoginCreateAndDelete(t *testing.T) {
	data, token := fakeCodexLogin(t)
	kube := newKube(t, nil)
	out, err := runCodexTest(t, kube, data, token, "--stdin")
	if err != nil {
		t.Fatal("cannot create the login Secret")
	}
	wantAll(t, out, "created", "key codex")
	out, err = runCodexTest(t, kube, "", token, "--remove")
	if err != nil {
		t.Fatal("cannot delete the login Secret")
	}
	wantAll(t, out, "deleted", "OpenAI account")
	if _, err := getLogin(t, kube); !apierrors.IsNotFound(err) {
		t.Fatal("the Secret is still present")
	}
}

func TestCodexLoginRejectsInputWithoutDisclosure(t *testing.T) {
	data, token := fakeCodexLogin(t)
	cases := []string{"", "null", "[]", "{}", token, data + data,
		strings.Replace(data, "chatgpt", token, 1),
		strings.Replace(data, "\"OPENAI_API_KEY\":null", "\"OPENAI_API_KEY\":\""+token+"\"", 1),
		strings.Replace(data, "refresh_token", "missing", 1),
		strings.Replace(data, "2026-10-22T00:00:00Z", token, 1),
		strings.Repeat(token, 300000/len(token))}
	for i, input := range cases {
		kube := newKube(t, nil)
		_, err := runCodexTest(t, kube, input, token, "--stdin")
		if err == nil {
			t.Fatalf("case %d accepted bad input", i)
		}
		if _, err := getLogin(t, kube); !apierrors.IsNotFound(err) {
			t.Fatal("bad input made a Secret")
		}
	}
	for _, cause := range []error{errors.New(token), apierrors.NewBadRequest(token)} {
		err := (&env{}).loginError("store the login", cause)
		if strings.Contains(err.Error(), token) {
			t.Fatal("the request error has login data")
		}
	}
	_, err := readCodexLogin(iotest.ErrReader(errors.New(token)))
	if err == nil || strings.Contains(err.Error(), token) {
		t.Fatal("the read error is not safe")
	}
	_, err = runCodexTest(t, newKube(t, nil), data, token)
	if err == nil || !strings.Contains(err.Error(), "--stdin") {
		t.Fatal("Codex login must require --stdin")
	}
	kube := newKube(t, nil, loginSecret(nil, map[string][]byte{"other": []byte(otherValue)}))
	for _, args := range [][]string{{"--stdin"}, {"--remove"}} {
		_, err = runCodexTest(t, kube, data, token, args...)
		if err == nil || !strings.Contains(err.Error(), "koodam did not make it") {
			t.Fatal("changed an unmanaged Secret")
		}
	}
}

func TestCodexLoginCopy(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the Workspace image runs on Linux")
	}
	cmd := exec.Command("bash", "../../images/workspace/test/codex_login_test.sh")
	if err := cmd.Run(); err != nil {
		t.Fatal("the Workspace Codex login checks failed")
	}
}

func runCodexTest(t *testing.T, kube client.WithWatch, data, token string, flags ...string) (string, error) {
	t.Helper()
	return runLogin(t, kube, data, token, append([]string{loginCommand, harnessCodex}, flags...)...)
}

func TestCodexLoginSizeAndTerminal(t *testing.T) {
	data, token := fakeCodexLogin(t)
	prefix := data[:len(data)-1] + `,"padding":"`
	for _, size := range []int{256*1024 - 1, 256*1024 + 1} {
		padded := prefix + strings.Repeat(" ", size-len(prefix)-2) + `"}`
		_, err := readCodexLogin(strings.NewReader(padded))
		if (err != nil) != (size > 256*1024) {
			t.Fatal("the input size limit is not correct")
		}
	}
	old := codexInputIsTerminal
	codexInputIsTerminal = func(io.Reader) bool { return true }
	t.Cleanup(func() { codexInputIsTerminal = old })
	kube := newKube(t, nil)
	_, err := runCodexTest(t, kube, data, token, "--stdin")
	if err == nil || !strings.Contains(err.Error(), "does not read from a terminal") {
		t.Fatal("the command accepted terminal input")
	}
	if _, err := getLogin(t, kube); !apierrors.IsNotFound(err) {
		t.Fatal("terminal input made a Secret")
	}
}
