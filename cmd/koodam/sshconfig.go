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
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"time"
	"unicode"
)

const (
	// includeLine is the line that koodam adds at the top of ~/.ssh/config. OpenSSH reads a
	// relative Include path from ~/.ssh.
	includeLine = "Include config.d/koodam"
	// entryMark starts the comment line before each host entry. NAMESPACE/WORKSPACE follows it.
	entryMark = "# koodam-workspace "
	// hostHeader is the first line of the host file.
	hostHeader = "# koodam ssh setup writes this file. Do not edit it: koodam writes it again.\n"
)

// onWindows is true on Windows. Windows has no file modes and no shell for the ProxyCommand.
const onWindows = runtime.GOOS == "windows"

// includeForms are the Include values that read the host file.
var includeForms = []string{"config.d/koodam", "~/.ssh/config.d/koodam", "config.d/*", "~/.ssh/config.d/*"}

// pubKeyLine is the type and the data of an OpenSSH public key, as in the Workspace API.
var pubKeyLine = regexp.MustCompile(`^(ssh-ed25519|ecdsa-sha2-nistp256|ecdsa-sha2-nistp384|` +
	`ecdsa-sha2-nistp521|sk-ssh-ed25519@openssh\.com|sk-ecdsa-sha2-nistp256@openssh\.com|ssh-rsa) ` +
	`[A-Za-z0-9+/]+={0,2}$`)

// sshPaths are the SSH files of the user.
type sshPaths struct{ dir string }

// userSSH gives the paths under ~/.ssh. On Windows, the home is the user profile folder.
func userSSH() (sshPaths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return sshPaths{}, fmt.Errorf("find the home folder: %w", err)
	}
	return sshPaths{dir: filepath.Join(home, ".ssh")}, nil
}

func (p sshPaths) key() string        { return filepath.Join(p.dir, "koodam_ed25519") }
func (p sshPaths) knownHosts() string { return filepath.Join(p.dir, "koodam_known_hosts") }
func (p sshPaths) hostDir() string    { return filepath.Join(p.dir, "config.d") }
func (p sshPaths) hostFile() string   { return filepath.Join(p.hostDir(), "koodam") }
func (p sshPaths) config() string     { return filepath.Join(p.dir, "config") }

// hostAlias is the long host name and the HostKeyAlias of a Workspace. A DNS label has no dot,
// so 2 different Workspaces never get the same alias.
func hostAlias(ns, ws string) string { return "koodam-" + ws + "." + ns }

// publicKey gives the type and the data of an OpenSSH public key line, without the comment.
// It refuses a line that is not 1 valid public key.
func publicKey(line string) (string, bool) {
	line = strings.TrimSpace(line)
	if strings.ContainsAny(line, "\r\n") {
		return "", false
	}
	f := strings.Fields(line)
	if len(f) < 2 || !pubKeyLine.MatchString(f[0]+" "+f[1]) {
		return "", false
	}
	return f[0] + " " + f[1], true
}

// writeFile writes data to path through a temporary file and a rename. If the file has the
// same data, writeFile changes only the mode, if it is not mode.
func writeFile(path string, data []byte, mode fs.FileMode) error {
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, data) {
		fi, err := os.Stat(path)
		if err != nil || fi.Mode().Perm() == mode {
			return err
		}
		return os.Chmod(path, mode)
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".koodam-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if err := f.Chmod(mode); err != nil && !onWindows {
		_ = f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// readOptional reads a file. A missing file gives no data and no error.
func readOptional(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return data, err
}

// setKnownHost writes the host key key for alias to the known_hosts file. If key is empty, it
// removes the line of alias.
func setKnownHost(path, alias, key string) error {
	data, err := readOptional(path)
	if err != nil {
		return err
	}
	var out []string
	for line := range strings.Lines(string(data)) {
		if f := strings.Fields(line); len(f) > 0 && f[0] == alias {
			continue
		}
		out = append(out, strings.TrimRight(line, "\r\n"))
	}
	if key != "" {
		out = append(out, alias+" "+key)
	}
	text := strings.Join(out, "\n")
	if text != "" {
		text += "\n"
	}
	return writeFile(path, []byte(text), 0o600)
}

// hostEntry is 1 Workspace in the host file. proxy is the ProxyCommand value. koodam keeps
// the value of each other entry, because each entry has the kube context of its setup.
type hostEntry struct{ target, proxy string }

// readHosts gives the entries in the host file. It ignores an entry with no ProxyCommand.
func readHosts(path string) ([]hostEntry, error) {
	data, err := readOptional(path)
	var entries []hostEntry
	target := ""
	for line := range strings.Lines(string(data)) {
		line = strings.TrimSpace(line)
		if t, ok := strings.CutPrefix(line, strings.TrimSpace(entryMark)); ok {
			target = strings.TrimSpace(t)
			if _, _, err := parseTarget(target); err != nil {
				target = ""
			}
		} else if p, ok := strings.CutPrefix(line, "ProxyCommand "); ok && target != "" {
			entries = append(entries, hostEntry{target: target, proxy: p})
			target = ""
		}
	}
	return entries, err
}

// readEntries gives the targets NAMESPACE/WORKSPACE in the host file.
func readEntries(path string) ([]string, error) {
	entries, err := readHosts(path)
	targets := make([]string, 0, len(entries))
	for _, e := range entries {
		targets = append(targets, e.target)
	}
	return targets, err
}

// setHost adds or replaces the entry e, or removes the entry of e.target if e.proxy is empty.
func setHost(entries []hostEntry, e hostEntry) []hostEntry {
	entries = slices.DeleteFunc(entries, func(o hostEntry) bool { return o.target == e.target })
	if e.proxy != "" {
		entries = append(entries, e)
	}
	return entries
}

// hostNames gives the Host names of each target. Each target has the long name. It also has
// the short name koodam-WORKSPACE if no other target has the same Workspace name.
func hostNames(targets []string) map[string]string {
	count := map[string]int{}
	for _, t := range targets {
		_, ws, _ := strings.Cut(t, "/")
		count[ws]++
	}
	names := map[string]string{}
	for _, t := range targets {
		ns, ws, _ := strings.Cut(t, "/")
		names[t] = hostAlias(ns, ws)
		if count[ws] == 1 {
			names[t] = "koodam-" + ws + " " + names[t]
		}
	}
	return names
}

// proxyArg checks and quotes 1 argument of the ProxyCommand. OpenSSH runs the ProxyCommand
// with the user shell, so the argument is in single quotes. On Windows, OpenSSH runs it with
// no shell, so the argument is in double quotes. OpenSSH expands % tokens, so each % becomes %%.
func proxyArg(what, s string) (string, error) {
	if s == "" || strings.ContainsFunc(s, unicode.IsControl) || (onWindows && strings.Contains(s, `"`)) {
		return "", fmt.Errorf("the %s is empty or has a character that koodam cannot quote", what)
	}
	q := "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
	if onWindows {
		q = `"` + s + `"`
	}
	return strings.ReplaceAll(q, "%", "%%"), nil
}

// proxyCommand gives the ProxyCommand value. It pins the kube context, and the kubeconfig if
// kubeconfig is not empty, so that a later context change does not change the cluster.
func proxyCommand(exe, kubeContext, kubeconfig, target string) (string, error) {
	args := []string{exe, "--context", kubeContext}
	if kubeconfig != "" {
		args = append(args, "--kubeconfig", kubeconfig)
	}
	what := []string{"path of the koodam program", "", "kube context name", "", "kubeconfig path"}
	for i := 0; i < len(args); i += 2 {
		q, err := proxyArg(what[i], args[i])
		if err != nil {
			return "", err
		}
		args[i] = q
	}
	return strings.Join(args, " ") + " tunnel " + target, nil
}

// renderHosts gives the host file for the entries.
func renderHosts(entries []hostEntry) []byte {
	entries = slices.SortedFunc(slices.Values(entries), func(a, b hostEntry) int {
		return strings.Compare(a.target, b.target)
	})
	targets := make([]string, 0, len(entries))
	for _, e := range entries {
		targets = append(targets, e.target)
	}
	names := hostNames(targets)
	var b strings.Builder
	b.WriteString(hostHeader)
	for _, e := range entries {
		ns, ws, _ := strings.Cut(e.target, "/")
		fmt.Fprintf(&b, "\n%s%s\nHost %s\n", entryMark, e.target, names[e.target])
		fmt.Fprintf(&b, "  User dev\n  IdentityFile ~/.ssh/koodam_ed25519\n  IdentitiesOnly yes\n")
		fmt.Fprintf(&b, "  UserKnownHostsFile ~/.ssh/koodam_known_hosts\n  HostKeyAlias %s\n", hostAlias(ns, ws))
		fmt.Fprintf(&b, "  ProxyCommand %s\n  ServerAliveInterval 30\n", e.proxy)
	}
	return []byte(b.String())
}

// hasInclude tells if the config reads the host file before the first Host or Match line.
// Only an Include before the first Host or Match line applies to all hosts.
func hasInclude(data []byte) bool {
	for line := range strings.Lines(string(data)) {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		switch strings.ToLower(f[0]) {
		case "host", "match":
			return false
		case "include":
			if slices.ContainsFunc(f[1:], func(a string) bool { return slices.Contains(includeForms, a) }) {
				return true
			}
		}
	}
	return false
}

// addInclude adds the Include line at the top of ~/.ssh/config. It asks first, if yes is
// false. It makes a backup of the file first, and it changes no other line. It returns true if
// the config has the Include line at the end.
func addInclude(p sshPaths, yes bool, in io.Reader, out io.Writer, now time.Time) (bool, error) {
	path := p.config()
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	data, err := readOptional(path)
	if err != nil || hasInclude(data) {
		return err == nil, err
	}
	_, _ = fmt.Fprintf(out, "koodam adds this line at the top of %s:\n  %s\n", path, includeLine)
	if !yes {
		_, _ = fmt.Fprint(out, "Add the line? [y/N] ")
		answer, _ := bufio.NewReader(in).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(answer)); a != "y" && a != "yes" {
			return false, nil
		}
	}
	mode := fs.FileMode(0o600)
	if data != nil {
		fi, err := os.Stat(path)
		if err != nil {
			return false, err
		}
		mode = fi.Mode().Perm()
		backup := path + ".koodam-backup-" + now.Format("20060102T150405")
		if err := writeFile(backup, data, 0o600); err != nil {
			return false, fmt.Errorf("make the backup: %w", err)
		}
		_, _ = fmt.Fprintf(out, "Backup: %s\n", backup)
	}
	return true, writeFile(path, append([]byte(includeLine+"\n"), data...), mode)
}
