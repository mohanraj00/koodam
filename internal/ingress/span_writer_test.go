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
	"slices"
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
// for it. entered gets 1 value for each Write call. left gets 1 value when each Write call
// has written its output.
type probeWriter struct {
	mu                       sync.Mutex
	buf                      bytes.Buffer
	calls, active, maxActive int
	mode                     int
	secret                   string
	gate                     chan struct{}
	entered                  chan struct{}
	left                     chan struct{}
}

func (w *probeWriter) Write(p []byte) (int, error) {
	if w.left != nil {
		defer func() { w.left <- struct{}{} }()
	}
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

func (w *probeWriter) output() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
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

// directWriter has no worker goroutine. The test calls writeOne as the worker.
func directWriter(out io.Writer) *spanWriter {
	return &spanWriter{
		out: out, wait: 5 * time.Second, admit: make(chan struct{}, spanAdmitLimit),
		jobs: make(chan spanJob), quit: make(chan struct{}),
	}
}

func liveJob(line []byte) spanJob {
	return spanJob{line: line, deadline: time.Now().Add(time.Hour), done: make(chan bool, 1)}
}

// recvWithin gives the next value from c. It stops the test if no value comes in 10 seconds.
func recvWithin[T any](t *testing.T, c chan T, msg string) T {
	t.Helper()
	select {
	case v := <-c:
		return v
	case <-time.After(10 * time.Second):
		t.Fatal(msg)
	}
	var zero T
	return zero
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

// The callers fill the admission set while the sink blocks. The test is the worker, so each step
// occurs in a known order. close does not wait for the sink, and it releases each caller. After
// close, no write is accepted and the worker claims no job. A job that the worker claimed before
// close can enter or complete the sink after close.
func TestSpanWriterAdmissionAndClose(t *testing.T) {
	p := &probeWriter{gate: make(chan struct{}), entered: make(chan struct{}, 1)}
	var once sync.Once
	release := func() { once.Do(func() { close(p.gate) }) }
	t.Cleanup(release)
	w := directWriter(p)
	w.wait = time.Hour
	t.Cleanup(w.close)
	line := testLine(t)

	var wg sync.WaitGroup
	for range spanAdmitLimit {
		wg.Go(func() {
			if !errors.Is(w.write(line), errSpanWrite) {
				t.Error("a waiting write did not give the fixed error at close")
			}
		})
	}
	// The first job enters the sink and blocks there. The test holds the other jobs, so each
	// caller keeps its admission slot until close.
	first := recvWithin(t, w.jobs, "a caller did not give its job")
	sunk := make(chan bool, 1)
	go func() {
		ok := w.writeOne(first)
		first.done <- ok
		sunk <- ok
	}()
	recvWithin(t, p.entered, "the first job did not enter the sink")
	held := make([]spanJob, 0, spanAdmitLimit-1)
	for range spanAdmitLimit - 1 {
		held = append(held, recvWithin(t, w.jobs, "a caller did not give its job"))
	}
	if len(w.admit) != spanAdmitLimit {
		t.Fatal("the callers did not fill the admission set")
	}
	over := make(chan error, 1)
	go func() { over <- w.write(line) }()
	if !errors.Is(recvWithin(t, over, "a write over the admission limit blocked"), errSpanWrite) {
		t.Fatal("a write over the admission limit was accepted")
	}
	select {
	case <-w.jobs:
		t.Fatal("a write over the admission limit gave a job")
	default:
	}

	closed, waited := make(chan struct{}), make(chan struct{})
	noStdOutput(t, func() {
		go func() { w.close(); close(closed) }()
		recvWithin(t, closed, "close blocked on a blocked sink")
		go func() { wg.Wait(); close(waited) }()
		recvWithin(t, waited, "close did not release each waiting caller")
		if !errors.Is(w.write(line), errSpanWrite) {
			t.Error("a write after close was accepted")
		}
	})
	if w.state.Load() != spanClosed|spanWriting {
		t.Fatal("close did not keep the claim of the blocked Write")
	}

	// The job that the worker claimed before close completes the sink after close.
	release()
	if !recvWithin(t, sunk, "the claimed Write did not return") {
		t.Fatal("the claimed Write did not complete after close")
	}
	noStdOutput(t, func() {
		if slices.ContainsFunc(held, w.writeOne) {
			t.Error("the worker claimed a held job after close")
		}
	})
	if calls, maxActive := p.counts(); calls != 1 || maxActive != 1 {
		t.Fatal("more than the 1 claimed job entered the sink")
	}
	if p.output() != string(line) {
		t.Fatal("the sink did not get 1 whole line")
	}
	if w.state.Load() != spanClosed {
		t.Fatal("the claimed Write did not release its claim")
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

// select can send a job when its timer and the worker are both ready. The worker must refuse the
// expired job, and the refusal must not stop the writer.
func TestSpanWriterWorkerRefusesExpiredJob(t *testing.T) {
	p := &probeWriter{}
	w := newTestWriter(t, p, 5*time.Second)
	line := testLine(t)
	noStdOutput(t, func() {
		j := spanJob{line: line, deadline: time.Now(), done: make(chan bool, 1)}
		w.jobs <- j
		if <-j.done {
			t.Error("the worker wrote an expired job")
		}
	})
	if calls, _ := p.counts(); calls != 0 {
		t.Fatal("an expired job reached the writer")
	}
	if w.write(line) != nil {
		t.Fatal("an expired job stopped the writer")
	}
	if calls, _ := p.counts(); calls != 1 || p.output() != string(line) {
		t.Fatal("the write after an expired job was not 1 whole line")
	}
}

// The worker can take a live job after close. close occurs before the claim, so the claim fails.
func TestSpanWriterCloseBeforeClaim(t *testing.T) {
	p := &probeWriter{}
	w := directWriter(p)
	line := testLine(t)
	w.close()
	noStdOutput(t, func() {
		if w.writeOne(liveJob(line)) {
			t.Error("the worker claimed a job after close")
		}
		if !errors.Is(w.write(line), errSpanWrite) {
			t.Error("a write after close was accepted")
		}
	})
	if calls, _ := p.counts(); calls != 0 {
		t.Fatal("the writer made a Write call after close")
	}
	if w.state.Load() != spanClosed {
		t.Fatal("a failed claim changed the state")
	}
}

// A job that the worker claimed before close can enter or complete its Write after close. Here
// it entered before close. close does not wait for it, the waiting caller gets the fixed error,
// and no later job is claimed.
func TestSpanWriterCloseDuringClaimedWrite(t *testing.T) {
	p := &probeWriter{
		gate: make(chan struct{}), entered: make(chan struct{}, 1), left: make(chan struct{}, 1),
	}
	var once sync.Once
	release := func() { once.Do(func() { close(p.gate) }) }
	t.Cleanup(release)
	w := newTestWriter(t, p, 5*time.Second)
	line := testLine(t)

	first := make(chan error, 1)
	go func() { first <- w.write(line) }()
	<-p.entered
	if w.state.Load() != spanWriting {
		t.Fatal("the worker entered Write without a claim")
	}
	noStdOutput(t, func() {
		w.close()
		if !errors.Is(<-first, errSpanWrite) {
			t.Error("close did not release the waiting caller")
		}
		if !errors.Is(w.write(line), errSpanWrite) {
			t.Error("a write after close was accepted")
		}
	})
	if w.state.Load() != spanClosed|spanWriting {
		t.Fatal("close did not keep the claim of the current Write")
	}

	release()
	<-p.left
	if calls, maxActive := p.counts(); calls != 1 || maxActive != 1 {
		t.Fatal("the writer made more than 1 Write call")
	}
	if p.output() != string(line) {
		t.Fatal("the claimed Write did not complete after close")
	}
}

// Callers write while close runs. Each write that begins after close fails, and the sink gets
// whole lines, 1 at a time. Only a job that the worker claimed before close can enter the sink
// after close, so the sink gets at most 1 Write call after close.
func TestSpanWriterCloseDuringAdmission(t *testing.T) {
	p := &probeWriter{entered: make(chan struct{}, 16*50)}
	w := newTestWriter(t, p, 5*time.Second)
	line := testLine(t)
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			for range 50 {
				_ = w.write(line)
			}
		})
	}
	recvWithin(t, p.entered, "no write entered the sink before close")
	w.close()
	atClose, _ := p.counts()
	for range 100 {
		if !errors.Is(w.write(line), errSpanWrite) {
			t.Fatal("a write begun after close was accepted")
		}
	}
	waited := make(chan struct{})
	go func() { wg.Wait(); close(waited) }()
	recvWithin(t, waited, "close did not release each caller")
	// After close no claim can start. When the last claim ends, the sink gets no more calls.
	for deadline := time.Now().Add(10 * time.Second); w.state.Load()&spanWriting != 0; {
		if time.Now().After(deadline) {
			t.Fatal("the claimed Write did not return")
		}
		time.Sleep(time.Millisecond)
	}
	calls, maxActive := p.counts()
	if calls > atClose+1 || maxActive != 1 {
		t.Fatal("the sink got more than 1 Write call after close or 2 Write calls at a time")
	}
	out := p.output()
	if !strings.HasSuffix(out, "\n") {
		t.Fatal("the sink holds a partial line")
	}
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) != calls {
		t.Fatal("the line count is not the Write call count")
	}
	for _, l := range lines {
		decodeLine(t, []byte(l+"\n"))
	}
}
