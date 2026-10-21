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
	"bytes"
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// fakeToken makes a token at run time, so that no token is in the source.
func fakeToken(fill string) string {
	return tokenPrefix + "01-" + strings.Repeat(fill, 80)
}

// runLogin runs koodam with stdin. It fails the test if the output or the error has the token.
func runLogin(t *testing.T, kube client.WithWatch, stdin, token string, args ...string) (string, error) {
	t.Helper()
	out := &bytes.Buffer{}
	root := newRoot(func() (*env, error) { return &env{kube: kube, namespace: testNS}, nil })
	root.SetOut(out)
	root.SetErr(out)
	root.SetIn(strings.NewReader(stdin))
	root.SetArgs(args)
	root.SetContext(context.Background())
	err := root.Execute()
	if token != "" && strings.Contains(out.String(), token) {
		t.Errorf("the output has the token:\n%s", out.String())
	}
	if token != "" && err != nil && strings.Contains(err.Error(), token) {
		t.Errorf("the error has the token: %v", err)
	}
	return out.String(), err
}

func getLogin(t *testing.T, kube client.Client) (*corev1.Secret, error) {
	t.Helper()
	s := &corev1.Secret{}
	return s, kube.Get(context.Background(), client.ObjectKey{Namespace: testNS, Name: loginName}, s)
}

func loginSecret(labels map[string]string, data map[string][]byte) *corev1.Secret {
	return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: testNS, Name: loginName, Labels: labels},
		Type: corev1.SecretTypeOpaque, Data: data}
}

func TestLoginCreatesThenUpdatesTheSecret(t *testing.T) {
	kube := newKube(t, nil)
	first := fakeToken("x")
	out, err := runLogin(t, kube, "  "+first+"\n", first, "login", "claude-code", "--stdin")
	if err != nil {
		t.Fatalf("login: %v\n%s", err, out)
	}
	wantAll(t, out, "secret/koodam-login created in namespace "+testNS, "key claude-code")
	s, err := getLogin(t, kube)
	if err != nil {
		t.Fatal(err)
	}
	if string(s.Data[harnessClaude]) != first || s.Type != corev1.SecretTypeOpaque ||
		s.Labels[managedByLabel] != managedBy {
		t.Errorf("got type %s, labels %v and a token that is not the input", s.Type, s.Labels)
	}

	// An update keeps the keys of the other harnesses.
	s.Data["other"] = []byte("keep")
	if err := kube.Update(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	second := fakeToken("y")
	out, err = runLogin(t, kube, second, second, "login", "claude-code", "--stdin")
	if err != nil {
		t.Fatalf("second login: %v\n%s", err, out)
	}
	wantAll(t, out, "secret/koodam-login updated")
	s, _ = getLogin(t, kube)
	if string(s.Data[harnessClaude]) != second || string(s.Data["other"]) != "keep" {
		t.Error("the update did not replace the token or did not keep the other key")
	}
}

func TestLoginReadsTheTerminal(t *testing.T) {
	token := fakeToken("t")
	old := readTerminal
	readTerminal = func() ([]byte, error) { return []byte(token), nil }
	t.Cleanup(func() { readTerminal = old })
	kube := newKube(t, nil)
	out, err := runLogin(t, kube, "", token, "login", "claude-code")
	if err != nil {
		t.Fatalf("login: %v\n%s", err, out)
	}
	wantAll(t, out, "no echo", "secret/koodam-login created")
	if s, _ := getLogin(t, kube); string(s.Data[harnessClaude]) != token {
		t.Error("the Secret does not have the token from the terminal")
	}
}

func TestLoginRefusesASecretThatKoodamDidNotMake(t *testing.T) {
	kube := newKube(t, nil, loginSecret(nil, map[string][]byte{"x": []byte("theirs")}))
	token := fakeToken("x")
	for _, args := range [][]string{{"login", "claude-code", "--stdin"}, {"login", "--remove", "claude-code"}} {
		_, err := runLogin(t, kube, token, token, args...)
		if err == nil || !strings.Contains(err.Error(), "koodam did not make it") {
			t.Errorf("%v: got %v, want a refusal", args, err)
		}
	}
	if s, _ := getLogin(t, kube); string(s.Data["x"]) != "theirs" || len(s.Data) != 1 {
		t.Error("the command changed a Secret that koodam did not make")
	}
}

func TestLoginRefusesABadToken(t *testing.T) {
	cases := map[string]string{
		"bad prefix": "sk-ant-api03-" + strings.Repeat("z", 80),
		"too short":  tokenPrefix + "01-zz",
		"too long":   tokenPrefix + strings.Repeat("z", maxToken),
		"empty":      " \n",
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			kube := newKube(t, nil)
			_, err := runLogin(t, kube, input, strings.TrimSpace(input), "login", "claude-code", "--stdin")
			if err == nil {
				t.Fatal("got no error")
			}
			if _, err := getLogin(t, kube); !apierrors.IsNotFound(err) {
				t.Errorf("the command made a Secret: %v", err)
			}
		})
	}
}

func TestLoginRemoveKeepsTheOtherKeys(t *testing.T) {
	token := fakeToken("x")
	kube := newKube(t, nil, loginSecret(map[string]string{managedByLabel: managedBy},
		map[string][]byte{harnessClaude: []byte(token), "other": []byte("keep")}))
	out, err := runLogin(t, kube, "", token, "login", "--remove", "claude-code")
	if err != nil {
		t.Fatalf("remove: %v\n%s", err, out)
	}
	wantAll(t, out, "key claude-code removed from secret/koodam-login", "Revoke it")
	s, err := getLogin(t, kube)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Data[harnessClaude]; ok || string(s.Data["other"]) != "keep" {
		t.Error("the remove did not delete only the claude-code key")
	}
}

func TestLoginRemoveDeletesTheSecretWithTheLastKey(t *testing.T) {
	token := fakeToken("x")
	kube := newKube(t, nil, loginSecret(map[string]string{managedByLabel: managedBy},
		map[string][]byte{harnessClaude: []byte(token)}))
	out, err := runLogin(t, kube, "", token, "login", "--remove", "claude-code")
	if err != nil {
		t.Fatalf("remove: %v\n%s", err, out)
	}
	wantAll(t, out, "secret/koodam-login deleted", "Revoke it")
	if _, err := getLogin(t, kube); !apierrors.IsNotFound(err) {
		t.Errorf("the Secret is still there: %v", err)
	}
	out, err = runLogin(t, kube, "", "", "login", "--remove", "claude-code")
	if err != nil {
		t.Fatalf("second remove: %v", err)
	}
	wantAll(t, out, "nothing to remove")
}

func TestLoginCodexStoresNothing(t *testing.T) {
	kube := newKube(t, nil)
	out, err := runLogin(t, kube, "", "", "login", "codex")
	if err != nil {
		t.Fatal(err)
	}
	wantAll(t, out, "koodam ssh <workspace>", "codex login --device-auth")
	if _, err := getLogin(t, kube); !apierrors.IsNotFound(err) {
		t.Errorf("codex login made a Secret: %v", err)
	}
}

func TestLoginRefusesAnUnknownHarness(t *testing.T) {
	_, err := runLogin(t, newKube(t, nil), "", "", "login", "gemini")
	if err == nil || !strings.Contains(err.Error(), "unknown harness") {
		t.Errorf("got %v, want an unknown harness error", err)
	}
}
