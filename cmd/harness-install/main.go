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

// Command harness-install downloads a coding harness binary into a folder and checks its sha256.
// It runs as an init container of an agent pod. It uses no credential.
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"runtime"
	"time"

	"github.com/mohanraj00/koodam/internal/harness"
)

func main() {
	name := flag.String("harness", "", "the harness: claude-code or codex")
	version := flag.String("version", "", "the version, for example 2.1.3. Empty is the latest version")
	libc := flag.String("libc", "glibc", "the libc of the agent image, glibc or musl. Claude Code only")
	dir := flag.String("dir", "", "the folder for the binary")
	timeout := flag.Duration("timeout", 10*time.Minute, "the time limit of the download")
	flag.Parse()
	if *name == "" || *dir == "" {
		flag.Usage()
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	in := &harness.Installer{HTTP: http.DefaultClient, ClaudeBase: harness.ClaudeBase, CodexAPI: harness.CodexAPI,
		Arch: runtime.GOARCH}
	got, err := in.Install(ctx, *name, *version, *libc, *dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "harness-install: %s: %v\n", *name, err)
		os.Exit(1)
	}
	fmt.Printf("harness-install: %s %s is in %s\n", *name, got, *dir)
}
