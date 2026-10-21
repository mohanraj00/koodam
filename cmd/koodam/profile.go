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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
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
	// keySep replaces "/" in a key, because a ConfigMap key cannot hold "/".
	keySep = "__"
	// maxGlobals is the maximum total size. The ConfigMap limit is 1 MiB.
	maxGlobals = 900 << 10
)

// globalFile is one file in the ConfigMap. Path is relative to /etc/koodam/globals after the
// pod rebuilds the tree, with "/" as the separator.
type globalFile struct {
	Key  string `json:"key"`
	Path string `json:"path"`
	Size int    `json:"size"`
	data string
}

// globalsManifest is the value of manifest.json.
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
			"The keys are CLAUDE.md, AGENTS.md and skills__NAME__PATH, with \"__\" for each \"/\" in PATH.\n" +
			"The key manifest.json lists each key and its path. The command refuses a name with \"__\"\n" +
			"or with \"_\" at the start or the end, a file that is not UTF-8 text, a credential file\n" +
			"(for example .credentials.json, auth.json, *.key, *.pem, id_*, .env, .netrc, or a file with\n" +
			"a private key) and a total size above 900 KiB.\n\n" +
			"It skips symlinks and files that are not regular, and shows each skipped path. Thus it\n" +
			"skips a skill folder that is a symlink. It refuses a CLAUDE.md or AGENTS.md symlink that\n" +
			"goes out of its folder. --dry-run does the same checks and does not connect to the cluster.",
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
	return readGlobal(root, filepath.Base(file), file, rel)
}

// readSkills reads each regular file under dir/NAME/ through an os.Root on dir. It follows a
// symlink only for dir. It skips each other symlink and shows its path.
func readSkills(cmd *cobra.Command, dir string) ([]globalFile, error) {
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	var files []globalFile
	err = fs.WalkDir(root.FS(), ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || name == "." {
			return err
		}
		shown := filepath.Join(dir, filepath.FromSlash(name))
		skip := ""
		switch {
		case d.IsDir() && d.Name() == ".git":
			return fs.SkipDir
		case d.IsDir():
			return nil
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
		f, err := readGlobal(root, name, shown, "skills/"+name)
		if err != nil {
			return err
		}
		files = append(files, f)
		return nil
	})
	return files, err
}

// checkRel refuses a path that the pod cannot rebuild as a safe and unique file. A key has "__"
// for each "/", so a segment must not hold "__" or start or end with "_".
func checkRel(rel string) error {
	if rel == "" || path.IsAbs(rel) || filepath.IsAbs(rel) {
		return errors.New("the path must be relative")
	}
	for _, p := range strings.Split(rel, "/") {
		switch {
		case p == "" || p == "." || p == "..":
			return fmt.Errorf("the segment %q is not permitted", p)
		case strings.Contains(p, keySep), strings.HasPrefix(p, "_"), strings.HasSuffix(p, "_"):
			return fmt.Errorf("a name with %q, or with \"_\" at the start or the end, is not permitted, "+
				"because %q replaces \"/\" in a key", keySep, keySep)
		}
	}
	return nil
}

// readGlobal reads name in root to the relative path rel. shown is the path in the errors. It
// refuses a credential file and a file that is not UTF-8 text. The errors never give the content.
func readGlobal(root *os.Root, name, shown, rel string) (globalFile, error) {
	if isCredential(path.Base(rel)) || isCredential(filepath.Base(name)) {
		return globalFile{}, fmt.Errorf("%s looks like a credential file: koodam profile push never pushes it", shown)
	}
	if err := checkRel(rel); err != nil {
		return globalFile{}, fmt.Errorf("%s: %w", shown, err)
	}
	key := strings.ReplaceAll(rel, "/", keySep)
	if errs := validation.IsConfigMapKey(key); len(errs) > 0 || key == manifestKey {
		return globalFile{}, fmt.Errorf("%s: %s is not a valid ConfigMap key (use only letters, digits, '-', '_' and '.')",
			shown, key)
	}
	f, err := root.Open(name)
	if err != nil {
		return globalFile{}, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return globalFile{}, err
	}
	if !info.Mode().IsRegular() {
		return globalFile{}, fmt.Errorf("%s is not a regular file", shown)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxGlobals+1))
	if err != nil {
		return globalFile{}, fmt.Errorf("read %s: %w", shown, err)
	}
	if len(data) > maxGlobals {
		return globalFile{}, fmt.Errorf("%s is larger than %d KiB", shown, maxGlobals>>10)
	}
	if !utf8.Valid(data) {
		return globalFile{}, fmt.Errorf("%s is not UTF-8 text: koodam profile push does not push binary files", shown)
	}
	if bytes.Contains(data, []byte("PRIVATE KEY-----")) {
		return globalFile{}, fmt.Errorf("%s has a private key: koodam profile push never pushes it", shown)
	}
	return globalFile{Key: key, Path: rel, Size: len(data), data: string(data)}, nil
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
		return nil, fmt.Errorf("the global files have %d KiB, and the limit is %d KiB: remove files or skills",
			total>>10, maxGlobals>>10)
	}
	return data, nil
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
