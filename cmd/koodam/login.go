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
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
	"golang.org/x/term"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// loginName is the Secret with 1 key for each harness login. The Workspace pod mounts it.
	loginName = "koodam-login"
	// harnessClaude is the harness name and the Secret key for the Claude Code token.
	harnessClaude = "claude-code"
	harnessCodex  = "codex"
	// tokenPrefix is the start of a token from claude setup-token.
	tokenPrefix = "sk-ant-oat"
	// minToken and maxToken are the permitted token lengths in bytes.
	minToken = 40
	maxToken = 1024
	// revokeNote tells the user that a removed token is still valid.
	revokeNote = "The token is still valid. Revoke it in your Anthropic account settings."
	// loginCreated and loginUpdated are the verbs in the output.
	loginCreated = "created"
	loginUpdated = "updated"
)

// errNotTerminal tells the user to use --stdin. It contains no input.
var errNotTerminal = errors.New("stdin is not a terminal: use --stdin to read the token from a pipe")

// readTerminal reads 1 line from the terminal with no echo. Tests replace it.
var readTerminal = func() ([]byte, error) {
	fd := int(os.Stdin.Fd()) //nolint:gosec // A file descriptor fits in an int.
	if !term.IsTerminal(fd) {
		return nil, errNotTerminal
	}
	return term.ReadPassword(fd)
}

func loginCmd(conn connect) *cobra.Command {
	var stdin, remove bool
	cmd := &cobra.Command{
		Use:   "login HARNESS",
		Short: "Store a harness login in the Secret koodam-login for the Workspaces in the namespace",
		Long: "koodam login claude-code reads a token from claude setup-token with no echo, or 1 line\n" +
			"from stdin with --stdin. It stores the token in the key claude-code of the Secret\n" +
			"koodam-login. It never shows the token.\n\n" +
			"koodam login codex stores nothing. It shows the login step in the Workspace.\n\n" +
			"--remove deletes the key. If no key is left, it deletes the Secret. The command\n" +
			"refuses a koodam-login Secret that koodam did not make.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			switch args[0] {
			case harnessCodex:
				if remove {
					_, _ = fmt.Fprintln(out, "koodam stores no Codex login. To log out, run codex logout in the Workspace.")
					return nil
				}
				_, _ = fmt.Fprintln(out, "koodam stores no Codex login. Codex keeps 1 login for each Workspace in its home.\n"+
					"Do these steps:\n  1. koodam ssh <workspace>\n  2. codex login --device-auth")
				return nil
			case harnessClaude:
			default:
				return fmt.Errorf("unknown harness %q: use %s or %s", args[0], harnessClaude, harnessCodex)
			}
			if remove {
				e, err := conn()
				if err != nil {
					return err
				}
				return e.removeLogin(cmd, harnessClaude)
			}
			e, err := conn()
			if err != nil {
				return err
			}
			token, err := readToken(cmd, stdin)
			if err != nil {
				return err
			}
			verb, err := e.storeLogin(cmd, harnessClaude, token)
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintf(out, "secret/%s %s in namespace %s: key %s\n", loginName, verb, e.namespace, harnessClaude)
			return nil
		},
	}
	cmd.Flags().BoolVar(&stdin, "stdin", false, "read the token as 1 line from stdin")
	cmd.Flags().BoolVar(&remove, "remove", false, "remove the login of the harness")
	return cmd
}

// readToken reads and checks the token. An error never contains the input.
func readToken(cmd *cobra.Command, stdin bool) ([]byte, error) {
	var raw []byte
	var err error
	if stdin {
		raw, err = bufio.NewReader(io.LimitReader(cmd.InOrStdin(), 4*maxToken)).ReadBytes('\n')
		if errors.Is(err, io.EOF) {
			err = nil
		}
	} else {
		_, _ = fmt.Fprint(cmd.ErrOrStderr(), "Paste the token from claude setup-token (no echo): ")
		raw, err = readTerminal()
		_, _ = fmt.Fprintln(cmd.ErrOrStderr())
	}
	switch {
	case errors.Is(err, errNotTerminal):
		return nil, errNotTerminal
	case err != nil:
		return nil, errors.New("read the token: the read failed")
	}
	token := bytes.TrimSpace(raw)
	switch {
	case len(token) == 0:
		return nil, errors.New("the token is empty: run claude setup-token, then paste its token")
	case !bytes.HasPrefix(token, []byte(tokenPrefix)):
		return nil, fmt.Errorf("the token does not start with %s: use the token from claude setup-token", tokenPrefix)
	case len(token) < minToken || len(token) > maxToken:
		return nil, fmt.Errorf("the token has %d bytes, but a token has %d to %d bytes", len(token), minToken, maxToken)
	}
	return token, nil
}

// storeLogin creates or updates the Secret, and keeps the keys of the other harnesses.
func (e *env) storeLogin(cmd *cobra.Command, key string, token []byte) (string, error) {
	ctx := cmd.Context()
	var verb, op string
	var notOurs bool
	err := retry.OnError(retry.DefaultRetry, func(err error) bool {
		return apierrors.IsConflict(err) || apierrors.IsAlreadyExists(err)
	}, func() error {
		s := &corev1.Secret{}
		op = "get the Secret"
		err := e.kube.Get(ctx, client.ObjectKey{Namespace: e.namespace, Name: loginName}, s)
		switch {
		case apierrors.IsNotFound(err):
			s = &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: e.namespace, Name: loginName,
				Labels: map[string]string{managedByLabel: managedBy}}, Type: corev1.SecretTypeOpaque}
			s.Data = map[string][]byte{key: token}
			verb, op = loginCreated, "create the Secret"
			return e.kube.Create(ctx, s)
		case err != nil:
			return err
		case s.Labels[managedByLabel] != managedBy:
			notOurs = true
			return nil
		}
		if s.Data == nil {
			s.Data = map[string][]byte{}
		}
		s.Data[key] = token
		verb, op = loginUpdated, "update the Secret"
		return e.kube.Update(ctx, s)
	})
	switch {
	case notOurs:
		return "", e.notOursError()
	case err != nil:
		return "", e.loginError(op, err)
	}
	return verb, nil
}

// removeLogin deletes the key. If no key is left, it deletes the Secret with a UID and a
// resourceVersion precondition, so it does not delete a Secret that changed after the read.
func (e *env) removeLogin(cmd *cobra.Command, key string) error {
	ctx := cmd.Context()
	out := cmd.OutOrStdout()
	var op, result string
	var notOurs, removed bool
	err := retry.OnError(retry.DefaultRetry, apierrors.IsConflict, func() error {
		removed = false
		s := &corev1.Secret{}
		op = "get the Secret"
		err := e.kube.Get(ctx, client.ObjectKey{Namespace: e.namespace, Name: loginName}, s)
		switch {
		case apierrors.IsNotFound(err):
			result = fmt.Sprintf("secret/%s is not in namespace %s: nothing to remove", loginName, e.namespace)
			return nil
		case err != nil:
			return err
		case s.Labels[managedByLabel] != managedBy:
			notOurs = true
			return nil
		}
		if _, ok := s.Data[key]; !ok {
			result = fmt.Sprintf("secret/%s in namespace %s has no key %s: nothing to remove", loginName, e.namespace, key)
			return nil
		}
		delete(s.Data, key)
		removed = true
		if len(s.Data) > 0 {
			op = "update the Secret"
			result = fmt.Sprintf("key %s removed from secret/%s in namespace %s", key, loginName, e.namespace)
			return e.kube.Update(ctx, s)
		}
		op = "delete the Secret"
		result = fmt.Sprintf("secret/%s deleted in namespace %s: key %s was the last key", loginName, e.namespace, key)
		uid, rv := s.UID, s.ResourceVersion
		err = e.kube.Delete(ctx, s, client.Preconditions{UID: &uid, ResourceVersion: &rv})
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	})
	switch {
	case notOurs:
		return e.notOursError()
	case err != nil:
		return e.loginError(op, err)
	}
	_, _ = fmt.Fprintln(out, result)
	if removed {
		_, _ = fmt.Fprintln(out, revokeNote)
	}
	return nil
}

func (e *env) notOursError() error {
	return fmt.Errorf("secret/%s in namespace %s has no label %s=%s, so koodam did not make it: "+
		"koodam does not change it", loginName, e.namespace, managedByLabel, managedBy)
}

// loginError cleans an API error. On Forbidden, it gives the verbs that the user needs.
func (e *env) loginError(op string, err error) error {
	err = apiError(op, err)
	switch {
	case apierrors.IsForbidden(err):
		return fmt.Errorf("%w: you need get, create, update and delete on secrets in namespace %s", err, e.namespace)
	case apierrors.IsConflict(err) || apierrors.IsAlreadyExists(err):
		return fmt.Errorf("%w: another client changed the Secret at the same time, so run the command again", err)
	}
	return err
}
