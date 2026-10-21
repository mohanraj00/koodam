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
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// globalsName is the ConfigMap that the Workspace pod mounts at /etc/koodam/globals.
	globalsName = "koodam-globals"
	// workspaceLabel lets the controller cache see the ConfigMap. The cache keeps only the
	// ConfigMaps that have this label. The value is empty, because all the Workspaces in the
	// namespace use the ConfigMap. It must match internal/controller.
	workspaceLabel = "koodam.dev/workspace"
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
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "push",
		Short: "Push CLAUDE.md, AGENTS.md and the skill folders to the ConfigMap koodam-globals",
		Long: "Push your global files to the ConfigMap koodam-globals in the namespace. Each Workspace\n" +
			"pod mounts it read-only at /etc/koodam/globals.\n\n" +
			"The keys are CLAUDE.md, AGENTS.md and skills__NAME__PATH, with \"__\" for each \"/\" in PATH.\n" +
			"The key manifest.json lists each key and its path. The command refuses a path with \"__\",\n" +
			"a file that is not UTF-8 text, a credential file (.credentials.json, auth.json, *.key,\n" +
			".env) and a total size above 900 KiB. It skips symlinks and files that are not regular.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			files, err := readGlobals(cmd, claudeMD, agentsMD, skills)
			if err != nil {
				return err
			}
			e, err := conn()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if dryRun {
				for _, f := range files {
					_, _ = fmt.Fprintf(out, "%s\t%d\n", f.Key, f.Size)
				}
				_, _ = fmt.Fprintf(out, "configmap/%s in namespace %s: %d files, %d bytes (dry run)\n",
					globalsName, e.namespace, len(files), totalSize(files))
				return nil
			}
			verb, err := e.pushGlobals(cmd, files)
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
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show the keys and the sizes, and do not change the cluster")
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
	for _, src := range []struct{ flag, path, def string }{
		{"claude-md", claudeMD, filepath.Join(home, ".claude", "CLAUDE.md")},
		{"agents-md", agentsMD, filepath.Join(home, ".codex", "AGENTS.md")},
	} {
		path := src.path
		if path == "" {
			path = src.def
		}
		f, err := readGlobal(path, filepath.Base(src.def))
		if errors.Is(err, fs.ErrNotExist) && src.path == "" {
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "skip %s: the file does not exist\n", path)
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

// readSkills reads each regular file under dir/NAME/. It follows a symlink only for dir.
func readSkills(cmd *cobra.Command, dir string) ([]globalFile, error) {
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, err
	}
	var files []globalFile
	err = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil || rel == "." {
			return err
		}
		rel = filepath.ToSlash(rel)
		switch {
		case d.IsDir() && d.Name() == ".git":
			return fs.SkipDir
		case d.IsDir():
			return nil
		case !d.Type().IsRegular():
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "skip %s: not a regular file\n", path)
			return nil
		case !strings.Contains(rel, "/"):
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "skip %s: not in a skill folder\n", path)
			return nil
		}
		f, err := readGlobal(path, "skills/"+rel)
		if err != nil {
			return err
		}
		files = append(files, f)
		return nil
	})
	return files, err
}

// readGlobal reads one file to the relative path rel. It refuses a credential file and a file
// that is not UTF-8 text. The errors give the path, never the content.
func readGlobal(path, rel string) (globalFile, error) {
	if isCredential(filepath.Base(path)) {
		return globalFile{}, fmt.Errorf("%s looks like a credential file: koodam profile push never pushes it", path)
	}
	parts := strings.Split(rel, "/")
	for _, p := range parts {
		if strings.Contains(p, keySep) {
			return globalFile{}, fmt.Errorf("%s: a name with %q is not permitted, because %q replaces \"/\" in a key",
				path, keySep, keySep)
		}
	}
	key := strings.Join(parts, keySep)
	if errs := validation.IsConfigMapKey(key); len(errs) > 0 || key == manifestKey {
		return globalFile{}, fmt.Errorf("%s: %s is not a valid ConfigMap key (use only letters, digits, '-', '_' and '.')",
			path, key)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return globalFile{}, err
	}
	if !info.Mode().IsRegular() {
		return globalFile{}, fmt.Errorf("%s is not a regular file", path)
	}
	if info.Size() > maxGlobals {
		return globalFile{}, fmt.Errorf("%s is larger than %d KiB", path, maxGlobals>>10)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return globalFile{}, err
	}
	if !utf8.Valid(data) {
		return globalFile{}, fmt.Errorf("%s is not UTF-8 text: koodam profile push does not push binary files", path)
	}
	return globalFile{Key: key, Path: rel, Size: len(data), data: string(data)}, nil
}

// isCredential is true for the file names of a login, a key or an environment file.
func isCredential(name string) bool {
	return slices.Contains([]string{".credentials.json", "auth.json", ".env"}, name) ||
		strings.HasPrefix(name, ".env.") || strings.HasSuffix(name, ".key")
}

func totalSize(files []globalFile) int {
	n := 0
	for _, f := range files {
		n += f.Size
	}
	return n
}

// globalsData makes the ConfigMap data with manifest.json. It refuses a total above 900 KiB.
func globalsData(files []globalFile) (map[string]string, error) {
	manifest, err := json.MarshalIndent(globalsManifest{Version: 1, Files: files}, "", "  ")
	if err != nil {
		return nil, err
	}
	data := map[string]string{manifestKey: string(manifest)}
	total := len(manifestKey) + len(manifest)
	for _, f := range files {
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
// removed on the laptop goes away in the pod.
func (e *env) pushGlobals(cmd *cobra.Command, files []globalFile) (string, error) {
	data, err := globalsData(files)
	if err != nil {
		return "", err
	}
	ctx := cmd.Context()
	cm := &corev1.ConfigMap{}
	err = e.kube.Get(ctx, client.ObjectKey{Namespace: e.namespace, Name: globalsName}, cm)
	verb := "updated"
	switch {
	case apierrors.IsNotFound(err):
		cm = &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: e.namespace, Name: globalsName}}
		verb = "created"
	case err != nil:
		return "", e.globalsError("get the ConfigMap", err)
	}
	if cm.Labels == nil {
		cm.Labels = map[string]string{}
	}
	cm.Labels[workspaceLabel] = ""
	cm.Labels["app.kubernetes.io/managed-by"] = "koodam"
	cm.Data, cm.BinaryData = data, nil
	if verb == "created" {
		err = e.kube.Create(ctx, cm)
	} else {
		err = e.kube.Update(ctx, cm)
	}
	if err != nil {
		return "", e.globalsError(strings.TrimSuffix(verb, "d")+" the ConfigMap", err)
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
