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
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
)

// sshProgram is the OpenSSH client.
const sshProgram = "ssh"

// runProgram runs a program with the stdin, stdout and stderr of cmd. It uses no shell.
// Tests replace it.
var runProgram = func(cmd *cobra.Command, name string, args ...string) error {
	c := exec.CommandContext(cmd.Context(), name, args...)
	c.Stdin, c.Stdout, c.Stderr = cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr()
	return c.Run()
}

// sshCmd connects to a Workspace. Its subcommands configure the SSH connection.
func sshCmd(conn connect) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ssh WORKSPACE",
		Short: "Connect to a Workspace with SSH and attach to the tmux session main",
		Long: "Connect to a Workspace with SSH and attach to the tmux session main.\n" +
			"Run koodam ssh setup WORKSPACE first.\n\n" +
			"The host name of a Workspace is koodam-WORKSPACE.NAMESPACE. If only 1 Workspace in\n" +
			"~/.ssh/config.d/koodam has the name WORKSPACE, koodam-WORKSPACE is also its host name.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, ns, ws, err := sshTarget(conn, args[0])
			if err != nil {
				return err
			}
			if err := needEntry(ns, ws); err != nil {
				return err
			}
			return runProgram(cmd, sshProgram, "-t", hostAlias(ns, ws), "tmux", "new-session", "-A", "-s", "main")
		},
	}
	cmd.AddCommand(sshSetupCmd(conn), sshRemoveCmd(conn))
	return cmd
}

// sshTarget connects and gives the namespace and the Workspace name.
func sshTarget(conn connect, ws string) (*env, string, string, error) {
	e, err := conn()
	if err != nil {
		return nil, "", "", err
	}
	ns, ws, err := parseTarget(e.namespace + "/" + ws)
	return e, ns, ws, err
}

// needEntry returns an error if the host file has no entry for the Workspace.
func needEntry(ns, ws string) error {
	p, err := userSSH()
	if err != nil {
		return err
	}
	targets, err := readEntries(p.hostFile())
	if err != nil {
		return err
	}
	if !slices.Contains(targets, ns+"/"+ws) {
		return fmt.Errorf("no SSH entry for Workspace %s/%s: run koodam ssh setup %s", ns, ws, ws)
	}
	return nil
}

func sshSetupCmd(conn connect) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "setup WORKSPACE",
		Short: "Configure the SSH connection to a Workspace. You can run it again",
		Long: "Configure the SSH connection to a Workspace:\n" +
			"  1. If ~/.ssh/koodam_ed25519 does not exist, make it with ssh-keygen.\n" +
			"  2. Add the public key to the Workspace spec.sshPublicKeys, if it is not there.\n" +
			"  3. Write the host key from the Workspace status to ~/.ssh/koodam_known_hosts.\n" +
			"  4. Write the host entry to ~/.ssh/config.d/koodam.\n" +
			"  5. Add 'Include config.d/koodam' at the top of ~/.ssh/config, after a yes.\n" +
			"koodam never reads the private key. You need get and patch on workspaces.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			e, ns, ws, err := sshTarget(conn, args[0])
			if err != nil {
				return err
			}
			return e.sshSetup(cmd, ns, ws, yes)
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "add the Include line to ~/.ssh/config with no question")
	return cmd
}

func (e *env) sshSetup(cmd *cobra.Command, ns, ws string, yes bool) error {
	out := cmd.OutOrStdout()
	p, err := userSSH()
	if err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return errors.New("find the path of the koodam program for the ProxyCommand")
	}
	proxy, err := proxyCommand(exe, e.context, e.kubeconfig, ns+"/"+ws)
	if err != nil {
		return err
	}
	key, err := ensureKey(cmd, p)
	if err != nil {
		return err
	}
	w, err := e.setKey(cmd.Context(), ns, ws, key, true, out)
	if err != nil {
		return err
	}
	if w.Status.HostKey == "" {
		return fmt.Errorf("workspace %s/%s is not ready: it has no host key. "+
			"Run koodam ssh setup %s again when it is Running", ns, ws, ws)
	}
	hostKey, ok := publicKey(w.Status.HostKey)
	if !ok {
		return fmt.Errorf("the host key in the status of Workspace %s/%s is not a valid public key", ns, ws)
	}
	if err := os.MkdirAll(p.hostDir(), 0o700); err != nil {
		return err
	}
	if err := setKnownHost(p.knownHosts(), hostAlias(ns, ws), hostKey); err != nil {
		return fmt.Errorf("write %s: %w", p.knownHosts(), err)
	}
	entries, err := readHosts(p.hostFile())
	if err != nil {
		return err
	}
	entries = setHost(entries, hostEntry{target: ns + "/" + ws, proxy: proxy})
	if err := writeFile(p.hostFile(), renderHosts(entries), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", p.hostFile(), err)
	}
	active, err := addInclude(p, yes, cmd.InOrStdin(), out, time.Now())
	if err != nil {
		return fmt.Errorf("add the Include line: %w", err)
	}
	if !active {
		_, _ = fmt.Fprintf(out, "The host entry for Workspace %s/%s is not active: ~/.ssh/config does not "+
			"include it.\nAdd this line at the top of ~/.ssh/config, before the first Host line:\n  %s\n",
			ns, ws, includeLine)
		return nil
	}
	targets, _ := readEntries(p.hostFile())
	host := strings.Fields(hostNames(targets)[ns+"/"+ws] + " " + hostAlias(ns, ws))[0]
	_, _ = fmt.Fprintf(out, "SSH is configured for Workspace %s/%s.\n"+
		"  Terminal: koodam ssh %s, or ssh %s\n  Desktop app: add an SSH connection to the host %s\n",
		ns, ws, ws, host, host)
	return nil
}

// ensureKey makes the key with ssh-keygen if it does not exist, and gives the public key.
// ssh-keygen asks for the passphrase. koodam never reads the private key.
func ensureKey(cmd *cobra.Command, p sshPaths) (string, error) {
	pub := p.key() + ".pub"
	if _, err := os.Stat(pub); errors.Is(err, fs.ErrNotExist) {
		if _, err := os.Stat(p.key()); err == nil {
			return "", fmt.Errorf("%s exists, but %s does not: make it with ssh-keygen -y -f %s > %s",
				p.key(), pub, p.key(), pub)
		}
		if err := os.MkdirAll(p.dir, 0o700); err != nil {
			return "", err
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Make the SSH key %s with ssh-keygen.\n", p.key())
		if err := runProgram(cmd, "ssh-keygen", "-t", "ed25519", "-f", p.key(), "-C", "koodam"); err != nil {
			return "", fmt.Errorf("ssh-keygen: %w", err)
		}
	}
	return readPublicKey(pub)
}

func readPublicKey(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	key, ok := publicKey(string(data))
	if !ok {
		return "", fmt.Errorf("%s is not 1 valid OpenSSH public key", path)
	}
	return key, nil
}

// setKey adds key to the Workspace spec.sshPublicKeys, or removes it if add is false. It
// patches the Workspace only if the list changes. The patch fails if the Workspace changed
// after the read.
func (e *env) setKey(ctx context.Context, ns, ws, key string, add bool, out io.Writer) (
	*corev1alpha1.Workspace, error) {
	w := &corev1alpha1.Workspace{}
	err := e.kube.Get(ctx, client.ObjectKey{Namespace: ns, Name: ws}, w)
	if apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("workspace %s does not exist in namespace %s", ws, ns)
	}
	if err != nil {
		return nil, apiError("get the Workspace", err)
	}
	same := func(k string) bool { n, _ := publicKey(k); return n == key }
	if slices.ContainsFunc(w.Spec.SSHPublicKeys, same) == add {
		return w, nil
	}
	old := w.DeepCopy()
	if add {
		w.Spec.SSHPublicKeys = append(w.Spec.SSHPublicKeys, key)
	} else {
		w.Spec.SSHPublicKeys = slices.DeleteFunc(w.Spec.SSHPublicKeys, same)
	}
	if err := e.kube.Patch(ctx, w, client.MergeFromWithOptions(old, client.MergeFromWithOptimisticLock{})); err != nil {
		return nil, apiError("patch the Workspace", err)
	}
	verb := map[bool]string{true: "Added", false: "Removed"}[add]
	_, _ = fmt.Fprintf(out, "%s your public key in Workspace %s/%s.\n", verb, ns, ws)
	return w, nil
}

func sshRemoveCmd(conn connect) *cobra.Command {
	var key bool
	cmd := &cobra.Command{
		Use:   "remove WORKSPACE",
		Short: "Remove the host entry and the host key of a Workspace. --key also removes your public key",
		Long: "Remove the host entry from ~/.ssh/config.d/koodam and the host key from\n" +
			"~/.ssh/koodam_known_hosts. With --key, also remove your public key from the Workspace.\n" +
			"The command does not remove the Include line or the key files.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			e, ns, ws, err := sshTarget(conn, args[0])
			if err != nil {
				return err
			}
			p, err := userSSH()
			if err != nil {
				return err
			}
			if key {
				k, err := readPublicKey(p.key() + ".pub")
				if err != nil {
					return err
				}
				if _, err := e.setKey(cmd.Context(), ns, ws, k, false, cmd.OutOrStdout()); err != nil {
					return err
				}
			}
			if err := setKnownHost(p.knownHosts(), hostAlias(ns, ws), ""); err != nil {
				return err
			}
			// The other entries keep their ProxyCommand, so remove writes no new program path.
			entries, err := readHosts(p.hostFile())
			if err != nil {
				return err
			}
			if left := setHost(slices.Clone(entries), hostEntry{target: ns + "/" + ws}); len(left) != len(entries) {
				if err := writeFile(p.hostFile(), renderHosts(left), 0o600); err != nil {
					return err
				}
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Removed the SSH entry for Workspace %s/%s.\n", ns, ws)
			return nil
		},
	}
	cmd.Flags().BoolVar(&key, "key", false, "also remove your public key from the Workspace")
	return cmd
}
