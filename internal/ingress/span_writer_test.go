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

package ingress

import (
	"io"
	"log"
	"os"
	"runtime"
	"sync"
	"testing"
)

// noStdOutput runs f and fails if f writes to stdout, stderr or the standard logger. It restores
// the exact prior stdout, stderr and logger writer, also if f calls t.Fatal or panics.
func noStdOutput(t *testing.T, f func()) {
	t.Helper()
	r, pw, err := os.Pipe()
	if err != nil {
		t.Fatal("no pipe")
	}
	stdout, stderr, logOut := os.Stdout, os.Stderr, log.Writer()
	restore := sync.OnceFunc(func() {
		os.Stdout, os.Stderr = stdout, stderr
		log.SetOutput(logOut)
		_ = pw.Close()
	})
	t.Cleanup(restore)
	got := make(chan int64, 1)
	go func() {
		n, _ := io.Copy(io.Discard, r)
		_ = r.Close()
		got <- n
	}()
	os.Stdout, os.Stderr = pw, pw
	log.SetOutput(pw)
	func() {
		defer restore()
		f()
	}()
	if <-got != 0 {
		t.Fatal("the writer wrote a diagnostic")
	}
}

func TestNoStdOutputRestores(t *testing.T) {
	stdout, stderr, logOut := os.Stdout, os.Stderr, log.Writer()
	restored := func() bool { return os.Stdout == stdout && os.Stderr == stderr && log.Writer() == logOut }
	// runtime.Goexit stops f as t.Fatal does. It runs in an owned goroutine, so no test is
	// stopped, skipped or failed; done is closed by a defer after the Goexit.
	t.Run("goexit", func(t *testing.T) {
		done := make(chan struct{})
		go func() {
			defer close(done)
			noStdOutput(t, runtime.Goexit)
		}()
		<-done
	})
	if !restored() {
		t.Fatal("the prior output was not restored after Goexit")
	}
	func() {
		defer func() { _ = recover() }()
		noStdOutput(t, func() { panic("fixed test panic") })
	}()
	if !restored() {
		t.Fatal("the prior output was not restored after a panic")
	}
}
