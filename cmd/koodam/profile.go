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
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// globalsName is the ConfigMap that the Workspace pod mounts at /etc/koodam/globals.
	globalsName = "koodam-globals"
	// workspaceLabel lets the controller cache see the ConfigMap. The cache keeps only the
	// ConfigMaps that have this label. The value is empty, because all the Workspaces in the
	// namespace use the ConfigMap. It must match internal/controller.
	workspaceLabel = "koodam.dev/workspace"
	// managedByLabel with the value managedBy marks a koodam-globals that koodam made.
	managedByLabel = "app.kubernetes.io/managed-by"
	managedBy      = "koodam"
	// manifestKey is the key with the list of the files and their paths.
	manifestKey = "manifest.json"
	// maxGlobals is the maximum total size. The ConfigMap limit is 1 MiB.
	maxGlobals = 900 << 10
	// maxSkill is the maximum size of 1 skill folder. A skill above the total limit can never
	// fit, so the push skips it. A smaller skill can fit with the other files.
	maxSkill = maxGlobals
	// largestShown is the number of skills that the total-size error names.
	largestShown = 3
)

// skipError is a reason to skip a skill folder: a binary file or a file that is too large. For
// CLAUDE.md and AGENTS.md it is an error. Each other error from readGlobal stops the push.
type skipError struct{ reason string }

func (e skipError) Error() string { return e.reason }

// globalFile is one file in the ConfigMap. Path is relative to /etc/koodam/globals after the
// pod rebuilds the tree, with "/" as the separator.
type globalFile struct {
	Key  string `json:"key"`
	Path string `json:"path"`
	Size int    `json:"size"`
	data string
}

// globalsManifest is the value of manifest.json, for example
// {"version":1,"files":[{"key":"skills_sreview_sSKILL.md","path":"skills/review/SKILL.md","size":12}]}.
// The pod writes the value of each key to its path under the globals tree. encodeKey gives the
// key format, and decodeKey gives the path of a key.
type globalsManifest struct {
	Version int          `json:"version"`
	Files   []globalFile `json:"files"`
}

// profileCmd groups the commands for the shared profile of the namespace.
func profileCmd(conn connect) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "profile",
		Short: "Manage the global files that each Workspace in the namespace gets",
	}
	cmd.AddCommand(profilePushCmd(conn))
	return cmd
}

func profilePushCmd(conn connect) *cobra.Command {
	var claudeMD, agentsMD, skills string
	var dryRun, force bool
	cmd := &cobra.Command{
		Use:   "push",
		Short: "Push CLAUDE.md, AGENTS.md and the skill folders to the ConfigMap koodam-globals",
		Long: "Push your global files to the ConfigMap koodam-globals in the namespace. Each Workspace\n" +
			"pod mounts it read-only at /etc/koodam/globals.\n\n" +
			"The key of a path is the path with \"_u\" for each \"_\", then \"_s\" for each \"/\". For\n" +
			"example, skills/my_skill/SKILL.md has the key skills_smy_uskill_sSKILL.md. The key\n" +
			"manifest.json lists each key and its path. The command refuses a credential file (for\n" +
			"example .credentials.json, auth.json, *.key, *.pem, id_*, .env, .netrc, or a file with a\n" +
			"private key) and pushes nothing. It refuses a total size above 900 KiB, and names the\n" +
			"largest skills.\n\n" +
			"It skips a skill folder that has a file that is not UTF-8 text, or that is larger than\n" +
			"900 KiB. It skips dot folders, dot files, symlinks and files that are not regular, and shows\n" +
			"each skipped path with the reason. Thus it skips a skill folder that is a symlink. It refuses\n" +
			"a CLAUDE.md or AGENTS.md that is not UTF-8 text, or a symlink that goes out of its folder.\n" +
			"--dry-run does the same checks and does not connect to the cluster.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			files, err := readGlobals(cmd, claudeMD, agentsMD, skills)
			if err != nil {
				return err
			}
			data, err := globalsData(files)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if dryRun {
				for _, f := range files {
					_, _ = fmt.Fprintf(out, "%s\t%d\n", f.Key, f.Size)
				}
				_, _ = fmt.Fprintf(out, "configmap/%s: %d files, %d bytes (dry run)\n",
					globalsName, len(files), totalSize(files))
				return nil
			}
			e, err := conn()
			if err != nil {
				return err
			}
			verb, err := e.pushGlobals(cmd, data, force)
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintf(out, "configmap/%s %s in namespace %s: %d files, %d bytes\n",
				globalsName, verb, e.namespace, len(files), totalSize(files))
			return nil
		},
	}
	cmd.Flags().StringVar(&claudeMD, "claude-md", "", "the CLAUDE.md file (default ~/.claude/CLAUDE.md)")
	cmd.Flags().StringVar(&agentsMD, "agents-md", "", "the AGENTS.md file (default ~/.codex/AGENTS.md)")
	cmd.Flags().StringVar(&skills, "skills", "", "the folder with 1 folder for each skill (default ~/.claude/skills)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "do the checks and show the keys and the sizes, with no cluster call")
	cmd.Flags().BoolVar(&force, "force", false, "replace a koodam-globals ConfigMap that koodam did not make")
	return cmd
}

// readGlobals reads the files and makes the manifest. A path from a flag must exist. If a
// default path does not exist, readGlobals skips it.
func readGlobals(cmd *cobra.Command, claudeMD, agentsMD, skills string) ([]globalFile, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("find the home folder: %w", err)
	}
	var files []globalFile
	for _, src := range []struct{ path, def string }{
		{claudeMD, filepath.Join(home, ".claude", "CLAUDE.md")},
		{agentsMD, filepath.Join(home, ".codex", "AGENTS.md")},
	} {
		p := src.path
		if p == "" {
			p = src.def
		}
		f, err := readSingle(p, filepath.Base(src.def))
		if errors.Is(err, fs.ErrNotExist) && src.path == "" {
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "skip %s: the file does not exist\n", p)
			continue
		}
		if err != nil {
			return nil, err
		}
		files = append(files, f)
	}
	dir := skills
	if dir == "" {
		dir = filepath.Join(home, ".claude", "skills")
	}
	sk, err := readSkills(cmd, dir)
	if errors.Is(err, fs.ErrNotExist) && skills == "" {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "skip %s: the folder does not exist\n", dir)
	} else if err != nil {
		return nil, err
	}
	files = append(files, sk...)
	if len(files) == 0 {
		return nil, errors.New("found no global files: use --claude-md, --agents-md or --skills")
	}
	return files, nil
}

// readSingle reads one file through an os.Root on its folder. Thus a symlink that goes out of
// the folder fails.
func readSingle(file, rel string) (globalFile, error) {
	root, err := os.OpenRoot(filepath.Dir(file))
	if err != nil {
		return globalFile{}, err
	}
	defer func() { _ = root.Close() }()
	return readGlobal(root, filepath.Base(file), file, rel, true)
}

// skillRead is the result of the walk for 1 skill folder. reason is empty if the push keeps it.
type skillRead struct {
	name, reason string
	files        []globalFile
	size         int
}

// walkSkills reads each regular file under dir/NAME/ through an os.Root on dir. dir must have
// no symlinks. It skips each symlink, dot folder and dot file, and shows its path. It never reads
// a dot folder. It refuses a dot file with a credential name. It scans all the files of a skill
// for a credential, also after it has a reason to skip the skill, so a credential file always
// stops the push. It keeps no data of a skill that has a reason to skip.
func walkSkills(cmd *cobra.Command, dir string) ([]*skillRead, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	var skills []*skillRead
	err = fs.WalkDir(root.FS(), ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || name == "." {
			return err
		}
		shown := filepath.Join(dir, filepath.FromSlash(name))
		dot := strings.HasPrefix(d.Name(), ".")
		skip := ""
		switch {
		case d.IsDir() && dot:
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "skip %s: a dot folder\n", shown)
			return fs.SkipDir
		case d.IsDir() && !strings.Contains(name, "/"):
			skills = append(skills, &skillRead{name: name})
			return nil
		case d.IsDir():
			return nil
		case dot && isCredential(d.Name()):
			return credentialError(shown)
		case dot:
			skip = "a dot file"
		case d.Type()&fs.ModeSymlink != 0:
			skip = "a symlink"
		case !d.Type().IsRegular():
			skip = "not a regular file"
		case !strings.Contains(name, "/"):
			skip = "not in a skill folder"
		}
		if skip != "" {
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "skip %s: %s\n", shown, skip)
			return nil
		}
		s := skills[len(skills)-1]
		// If the skill has a reason to skip, readGlobal only does the credential checks.
		f, err := readGlobal(root, name, shown, "skills/"+name, s.reason == "")
		var se skipError
		switch {
		case errors.As(err, &se):
			s.skip(se.reason)
		case err != nil:
			return err
		case s.reason == "":
			s.files = append(s.files, f)
			s.size += f.Size
			if s.size > maxSkill {
				s.skip(fmt.Sprintf("the skill is larger than %d KiB, the limit for 1 skill", maxSkill>>10))
			}
		}
		return nil
	})
	return skills, err
}

// skip gives the skill its first reason to skip, and releases its data.
func (s *skillRead) skip(reason string) {
	if s.reason == "" {
		s.reason = reason
	}
	s.files = nil
}

// readSkills reads the skills in dir with walkSkills. It shows each skipped skill with the reason,
// and gives the files of the other skills.
func readSkills(cmd *cobra.Command, dir string) ([]globalFile, error) {
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, err
	}
	skills, err := walkSkills(cmd, dir)
	if err != nil {
		return nil, err
	}
	var files []globalFile
	for _, s := range skills {
		if s.reason != "" {
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "skip the skill %s: %s\n", filepath.Join(dir, s.name), s.reason)
			continue
		}
		files = append(files, s.files...)
	}
	return files, nil
}

func credentialError(shown string) error {
	return fmt.Errorf("%s looks like a credential file: koodam profile push never pushes it", shown)
}

// checkRel refuses a path that the pod cannot rebuild as a safe file: an absolute path, and a
// path with a "..", "." or empty segment.
func checkRel(rel string) error {
	if rel == "" || path.IsAbs(rel) || filepath.IsAbs(rel) {
		return errors.New("the path must be relative")
	}
	for p := range strings.SplitSeq(rel, "/") {
		if p == "" || p == "." || p == ".." {
			return fmt.Errorf("the segment %q is not permitted", p)
		}
	}
	return nil
}

// encodeKey makes the ConfigMap key of a relative path. A key cannot hold "/". First each "_"
// becomes "_u", then each "/" becomes "_s". After this, each "_" in the key starts "_u" or "_s",
// so decodeKey gives the path again and 2 paths never give the same key. For example,
// skills/review/__init__.py becomes skills_sreview_s_u_uinit_u_u.py.
func encodeKey(rel string) string {
	return strings.ReplaceAll(strings.ReplaceAll(rel, "_", "_u"), "/", "_s")
}

// decodeKey is the reverse of encodeKey. The pod can use manifest.json, or this rule.
func decodeKey(key string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(key); i++ {
		if key[i] != '_' {
			b.WriteByte(key[i])
			continue
		}
		if i+1 == len(key) {
			return "", fmt.Errorf("the key %s ends with \"_\"", key)
		}
		i++
		switch key[i] {
		case 'u':
			b.WriteByte('_')
		case 's':
			b.WriteByte('/')
		default:
			return "", fmt.Errorf("the key %s has \"_\" that is not \"_u\" or \"_s\"", key)
		}
	}
	return b.String(), nil
}

// readGlobal reads name in root to the relative path rel. shown is the path in the errors. It
// refuses a credential file. It gives a skipError for a file that is not UTF-8 text or that is
// larger than maxGlobals, after the credential checks. If keep is false, it does only the
// credential checks, keeps no data and gives an empty globalFile. The errors never give the content.
func readGlobal(root *os.Root, name, shown, rel string, keep bool) (globalFile, error) {
	if isCredential(path.Base(rel)) || isCredential(filepath.Base(name)) {
		return globalFile{}, credentialError(shown)
	}
	if !keep {
		return globalFile{}, scanGlobal(root, name, shown)
	}
	if err := checkRel(rel); err != nil {
		return globalFile{}, fmt.Errorf("%s: %w", shown, err)
	}
	key := encodeKey(rel)
	if errs := validation.IsConfigMapKey(key); len(errs) > 0 || key == manifestKey {
		return globalFile{}, fmt.Errorf("%s: %s is not a valid ConfigMap key (use only letters, digits, '-', '_' and '.')",
			shown, key)
	}
	data, err := openScan(root, name, shown, maxGlobals+1)
	if err != nil {
		return globalFile{}, err
	}
	if len(data) > maxGlobals {
		return globalFile{}, skipError{fmt.Sprintf("%s is larger than %d KiB", shown, maxGlobals>>10)}
	}
	if !utf8.Valid(data) {
		return globalFile{}, skipError{fmt.Sprintf(
			"%s is not UTF-8 text: koodam profile push does not push binary files", shown)}
	}
	return globalFile{Key: key, Path: rel, Size: len(data), data: string(data)}, nil
}

// scanGlobal refuses a file with a private key. It keeps no data.
func scanGlobal(root *os.Root, name, shown string) error {
	_, err := openScan(root, name, shown, 0)
	return err
}

// openScan opens a regular file, refuses it if it has a private key, and gives the first keep
// bytes.
func openScan(root *os.Root, name, shown string, keep int) ([]byte, error) {
	f, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", shown)
	}
	data, privateKey, err := readScan(f, keep)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", shown, err)
	}
	if privateKey {
		return nil, fmt.Errorf("%s has a private key: koodam profile push never pushes it", shown)
	}
	return data, nil
}

// privateKeyMark is in each PEM private key, for example "-----BEGIN OPENSSH PRIVATE KEY-----".
var privateKeyMark = []byte("PRIVATE KEY-----")

// readScan reads r to the end and looks for privateKeyMark in all of it, also in a binary file
// and after keep bytes. It keeps only the first keep bytes, so the memory stays small.
func readScan(r io.Reader, keep int) ([]byte, bool, error) {
	var data []byte
	buf := make([]byte, 64<<10)
	// tail is the end of the last block, so the scan finds a mark across 2 blocks.
	var tail []byte
	for {
		n, err := r.Read(buf)
		block := append(tail, buf[:n]...)
		if bytes.Contains(block, privateKeyMark) {
			return nil, true, nil
		}
		tail = append([]byte(nil), block[max(0, len(block)-len(privateKeyMark)+1):]...)
		if len(data) < keep {
			data = append(data, buf[:min(n, keep-len(data))]...)
		}
		if errors.Is(err, io.EOF) {
			return data, false, nil
		}
		if err != nil {
			return nil, false, err
		}
	}
}

// credentialNames are the file names of a login, a token or a key, in lower case.
var credentialNames = []string{".credentials.json", "credentials.json", "auth.json", "token.json", ".env",
	".netrc", ".npmrc", ".pypirc", ".git-credentials"}

// isCredential is true for the name of a credential file. It ignores the case.
func isCredential(name string) bool {
	n := strings.ToLower(name)
	return slices.Contains(credentialNames, n) || strings.HasPrefix(n, ".env.") || strings.HasPrefix(n, "id_") ||
		slices.ContainsFunc([]string{".key", ".pem", ".p12", ".pfx"}, func(s string) bool {
			return strings.HasSuffix(n, s)
		})
}

func totalSize(files []globalFile) int {
	n := 0
	for _, f := range files {
		n += f.Size
	}
	return n
}

// globalsData makes the ConfigMap data with manifest.json. It refuses a duplicate key and a total
// above 900 KiB.
func globalsData(files []globalFile) (map[string]string, error) {
	manifest, err := json.MarshalIndent(globalsManifest{Version: 1, Files: files}, "", "  ")
	if err != nil {
		return nil, err
	}
	data := map[string]string{manifestKey: string(manifest)}
	total := len(manifestKey) + len(manifest)
	for _, f := range files {
		if _, ok := data[f.Key]; ok {
			return nil, fmt.Errorf("2 files give the key %s: rename 1 of them", f.Key)
		}
		data[f.Key] = f.data
		total += len(f.Key) + f.Size
	}
	if total > maxGlobals {
		return nil, fmt.Errorf("the global files have %d KiB, and the limit is %d KiB: remove files or skills%s",
			total>>10, maxGlobals>>10, largestSkills(files))
	}
	return data, nil
}

// largestSkills gives the names and the sizes of the largest skills in files, for an error. It
// never gives the content.
func largestSkills(files []globalFile) string {
	sizes := map[string]int{}
	for _, f := range files {
		if parts := strings.SplitN(f.Path, "/", 3); len(parts) == 3 && parts[0] == "skills" {
			sizes[parts[1]] += f.Size
		}
	}
	names := slices.Collect(maps.Keys(sizes))
	slices.SortFunc(names, func(a, b string) int {
		return cmp.Or(cmp.Compare(sizes[b], sizes[a]), cmp.Compare(a, b))
	})
	if len(names) == 0 {
		return ""
	}
	shown := make([]string, 0, largestShown)
	for _, n := range names[:min(len(names), largestShown)] {
		shown = append(shown, fmt.Sprintf("%s (%d KiB)", n, sizes[n]>>10))
	}
	return ". The largest skills are " + strings.Join(shown, ", ")
}

// pushGlobals creates or updates the ConfigMap. It replaces all the data, so a file that you
// removed on the laptop goes away in the pod. On a conflict, it reads the ConfigMap again.
func (e *env) pushGlobals(cmd *cobra.Command, data map[string]string, force bool) (string, error) {
	ctx := cmd.Context()
	var verb, op string
	var notOurs bool
	err := retry.OnError(retry.DefaultRetry, func(err error) bool {
		return apierrors.IsConflict(err) || apierrors.IsAlreadyExists(err)
	}, func() error {
		cm := &corev1.ConfigMap{}
		op = "get the ConfigMap"
		err := e.kube.Get(ctx, client.ObjectKey{Namespace: e.namespace, Name: globalsName}, cm)
		switch {
		case apierrors.IsNotFound(err):
			cm = &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: e.namespace, Name: globalsName}}
			verb = "created"
		case err != nil:
			return err
		case cm.Labels[managedByLabel] != managedBy && !force:
			notOurs = true
			return nil
		default:
			verb = "updated"
		}
		if cm.Labels == nil {
			cm.Labels = map[string]string{}
		}
		cm.Labels[workspaceLabel] = ""
		cm.Labels[managedByLabel] = managedBy
		cm.Data, cm.BinaryData = data, nil
		if verb == "created" {
			op = "create the ConfigMap"
			return e.kube.Create(ctx, cm)
		}
		op = "update the ConfigMap"
		return e.kube.Update(ctx, cm)
	})
	switch {
	case notOurs:
		return "", fmt.Errorf("configmap/%s in namespace %s has no label %s=%s, so koodam did not make it: "+
			"delete it first, or use --force to replace it", globalsName, e.namespace, managedByLabel, managedBy)
	case apierrors.IsConflict(err) || apierrors.IsAlreadyExists(err):
		return "", fmt.Errorf("%w: another client changed the ConfigMap at the same time, so run the command again",
			apiError(op, err))
	case err != nil:
		return "", e.globalsError(op, err)
	}
	return verb, nil
}

// globalsError cleans an API error. On Forbidden, it gives the verbs that the user needs.
func (e *env) globalsError(op string, err error) error {
	err = apiError(op, err)
	if apierrors.IsForbidden(err) {
		return fmt.Errorf("%w: you need get, create and update on configmaps in namespace %s", err, e.namespace)
	}
	return err
}
