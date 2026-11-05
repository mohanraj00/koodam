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
	"bytes"
	"errors"
	"io"
	"log"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	modeOK = iota
	modeFail
	modeShort
	modePanic
)

// probeWriter counts Write calls and concurrent Write calls. If gate is set, each Write waits
// for it. entered gets 1 value for each Write call.
type probeWriter struct {
	mu                       sync.Mutex
	buf                      bytes.Buffer
	calls, active, maxActive int
	mode                     int
	secret                   string
	gate                     chan struct{}
	entered                  chan struct{}
}

func (w *probeWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	w.calls++
	w.active++
	w.maxActive = max(w.maxActive, w.active)
	w.mu.Unlock()
	if w.entered != nil {
		w.entered <- struct{}{}
	}
	if w.gate != nil {
		<-w.gate
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.active--
	switch w.mode {
	case modeFail:
		return 0, errors.New(w.secret)
	case modeShort:
		w.buf.Write(p[:len(p)-1])
		return len(p) - 1, nil
	case modePanic:
		panic(w.secret)
	}
	return w.buf.Write(p)
}

func (w *probeWriter) counts() (calls, maxActive int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.calls, w.maxActive
}

func testLine(t *testing.T) []byte {
	t.Helper()
	b, err := encodeSpan(testProxyNS, okObservation(), time.Now())
	if err != nil {
		t.Fatal("a valid span gave an error")
	}
	return b
}

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

func newTestWriter(t *testing.T, out io.Writer, wait time.Duration) *spanWriter {
	t.Helper()
	w, err := newSpanWriter(out)
	if err != nil {
		t.Fatal("the writer did not start")
	}
	w.wait = wait
	t.Cleanup(w.close)
	return w
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

func TestSpanWriterRejectsNil(t *testing.T) {
	if w, err := newSpanWriter(nil); w != nil || !errors.Is(err, errSpanWrite) {
		t.Fatal("a nil writer was accepted")
	}
}

func TestSpanWriterRejectsBadLines(t *testing.T) {
	p := &probeWriter{}
	w := newTestWriter(t, p, 5*time.Second)
	for _, line := range [][]byte{nil, []byte(`{}`), []byte("{}\n{}\n")} {
		if !errors.Is(w.write(line), errSpanWrite) {
			t.Fatal("a line that is not 1 whole line was accepted")
		}
	}
	if calls, _ := p.counts(); calls != 0 {
		t.Fatal("a bad line reached the writer")
	}
}

func TestSpanWriterConcurrentLines(t *testing.T) {
	p := &probeWriter{}
	w := newTestWriter(t, p, 5*time.Second)
	line := testLine(t)
	var wg sync.WaitGroup
	for range 32 {
		wg.Go(func() {
			for range 10 {
				if w.write(line) != nil {
					t.Error("a write failed")
				}
			}
		})
	}
	wg.Wait()
	calls, maxActive := p.counts()
	if calls != 320 || maxActive != 1 {
		t.Fatal("the writes were not 1 at a time with 1 call for each line")
	}
	lines := strings.Split(strings.TrimSuffix(p.buf.String(), "\n"), "\n")
	if len(lines) != 320 {
		t.Fatal("the line count is not the write count")
	}
	for _, l := range lines {
		decodeLine(t, []byte(l+"\n"))
	}
}

func TestSpanWriterFailureIsFinal(t *testing.T) {
	for _, tc := range []struct {
		name string
		mode int
	}{{litCaseError, modeFail}, {"short", modeShort}, {"panic", modePanic}} {
		t.Run(tc.name, func(t *testing.T) {
			p := &probeWriter{mode: tc.mode, secret: spanCanary()}
			w := newTestWriter(t, p, 5*time.Second)
			line := testLine(t)
			noStdOutput(t, func() {
				for range 3 {
					err := w.write(line)
					if !errors.Is(err, errSpanWrite) || strings.Contains(err.Error(), p.secret) {
						t.Error("a failed write did not give the fixed error")
					}
				}
			})
			if calls, _ := p.counts(); calls != 1 {
				t.Fatal("the writer tried again after a failed write")
			}
		})
	}
}

func TestSpanWriterBlockedIsBounded(t *testing.T) {
	p := &probeWriter{gate: make(chan struct{}), entered: make(chan struct{}, 1000)}
	var once sync.Once
	release := func() { once.Do(func() { close(p.gate) }) }
	t.Cleanup(release)
	w := newTestWriter(t, p, 2*time.Second)
	line := testLine(t)

	first := make(chan error, 1)
	go func() { first <- w.write(line) }()
	<-p.entered
	// The first caller read wait before the worker took its line, so this change is safe.
	w.wait = 50 * time.Millisecond
	start := time.Now()
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			if !errors.Is(w.write(line), errSpanWrite) {
				t.Error("a write behind a blocked write did not give the fixed error")
			}
		})
	}
	wg.Wait()
	if time.Since(start) > 10*time.Second {
		t.Fatal("a caller waited without bound")
	}
	if !errors.Is(<-first, errSpanWrite) {
		t.Fatal("a blocked write did not give the fixed error")
	}
	if calls, maxActive := p.counts(); calls != 1 || maxActive != 1 {
		t.Fatal("a blocked writer got more than 1 Write call")
	}

	// The blocked Write completes: the writer stays usable, with 1 worker.
	release()
	w.wait = 5 * time.Second
	if w.write(line) != nil {
		t.Fatal("a write after the release failed")
	}
	if calls, maxActive := p.counts(); calls != 2 || maxActive != 1 {
		t.Fatal("the writer did not use 1 worker for each write")
	}
}

// An idle worker can take a job after quit is closed. close must still refuse every later line.
func TestSpanWriterIdleRefusesAfterClose(t *testing.T) {
	p := &probeWriter{}
	w := newTestWriter(t, p, 5*time.Second)
	line := testLine(t)
	if w.write(line) != nil {
		t.Fatal("a write before close failed")
	}
	w.close()
	noStdOutput(t, func() {
		for range 200 {
			if !errors.Is(w.write(line), errSpanWrite) {
				t.Error("a write after close was accepted")
				return
			}
		}
	})
	if calls, _ := p.counts(); calls != 1 {
		t.Fatal("the writer made a Write call after close")
	}
}
