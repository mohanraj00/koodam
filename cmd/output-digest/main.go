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

// The output-digest command runs as a sidecar container of an Agent run pod. It waits until the
// kubelet stops it, which occurs after the agent container ends. Then it writes the SHA-256
// digest and the size of the output file to its termination message. It never writes the
// content of the file.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/mohanraj00/koodam/internal/outputdigest"
)

func main() {
	path := flag.String("path", "", "The absolute path of the output file.")
	result := flag.String("result", "/dev/termination-log", "The file that gets the result.")
	flag.Parse()
	if *path == "" {
		fmt.Fprintln(os.Stderr, "--path is necessary")
		os.Exit(2)
	}
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)
	<-stop
	if err := os.WriteFile(*result, outputdigest.File(*path).Encode(), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, "The result file is not writable.")
		os.Exit(1)
	}
}
