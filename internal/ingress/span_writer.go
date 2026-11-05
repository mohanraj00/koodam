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
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

// errSpanWrite is the only error of spanWriter. It holds no writer error text.
var errSpanWrite = errors.New("ingress span: the record was not written")

// Implementation limits. They are not contract values or public settings.
const (
	spanAdmitLimit = 64                     // callers that can wait at the same time
	spanWaitLimit  = 250 * time.Millisecond // the longest wait of 1 caller
)

// Bits of spanWriter.state. The worker can claim a job only when the state is 0.
const (
	spanClosed  uint32 = 1 << iota // close was called
	spanFailed                     // a Write failed, was short or panicked
	spanWriting                    // the worker claimed a job, and its Write did not return
)

type spanJob struct {
	line     []byte
	deadline time.Time // the monotonic deadline of the caller
	done     chan bool // capacity 1, so the worker never blocks on it
}

// spanWriter writes span lines to 1 io.Writer, for example os.Stdout. 1 worker goroutine, started
// once, does all writes in sequence with 1 Write call for each line. A caller waits at most wait.
// No line waits in a queue: a caller that cannot give its line to the worker in time gets
// errSpanWrite, and the line is dropped. After a failed, short or panicked Write, the writer
// refuses all later lines, because the output can hold a partial line.
//
// The write start of a job is its claim: the worker changes state from 0 to spanWriting with
// 1 compare-and-swap. close sets spanClosed in the same state, so each claim occurs before or
// after close, never at the same time. After close, no claim is possible. A job that the worker
// claimed before close can start or complete its Write call after close.
//
// Go cannot stop a Write call that blocks. Then the worker and its 1 line stay until Write
// returns. Other callers get errSpanWrite in the wait limit. close does not wait for the worker.
// After close, write refuses each line before admission, and the worker claims no new job.
type spanWriter struct {
	out   io.Writer
	wait  time.Duration
	admit chan struct{}
	jobs  chan spanJob
	quit  chan struct{}
	once  sync.Once
	state atomic.Uint32
}

func newSpanWriter(out io.Writer) (*spanWriter, error) {
	if out == nil {
		return nil, errSpanWrite
	}
	w := &spanWriter{
		out: out, wait: spanWaitLimit, admit: make(chan struct{}, spanAdmitLimit),
		jobs: make(chan spanJob), quit: make(chan struct{}),
	}
	go w.run()
	return w, nil
}

func (w *spanWriter) run() {
	for {
		select {
		case <-w.quit:
			return
		case j := <-w.jobs:
			j.done <- w.writeOne(j)
		}
	}
}

// claim starts the write of 1 job. It is false after close, after a failed Write, or if the job
// expired. The deadline check occurs after the compare-and-swap, so a pause before or after the
// swap cannot make an expired job write: a job that is live at the check was live at the claim.
// An expired job releases the claim and does not set spanFailed.
func (w *spanWriter) claim(j spanJob) bool {
	if !w.state.CompareAndSwap(0, spanWriting) {
		return false
	}
	if !time.Now().Before(j.deadline) {
		w.state.And(^spanWriting)
		return false
	}
	return true
}

// writeOne makes at most 1 Write call, and only for a claimed job. It sets spanFailed before it
// releases the claim, so no other job can see a 0 state after a failed Write.
func (w *spanWriter) writeOne(j spanJob) (ok bool) {
	if !w.claim(j) {
		return false
	}
	defer func() {
		if recover() != nil || !ok {
			w.state.Or(spanFailed)
		}
		w.state.And(^spanWriting)
	}()
	n, err := w.out.Write(j.line)
	return err == nil && n == len(j.line)
}

// write gives 1 line that ends with its only newline to the worker. It returns nil only after
// a complete Write. It writes no diagnostic, and it does not retry or fall back.
func (w *spanWriter) write(line []byte) error {
	if len(line) == 0 || bytes.IndexByte(line, '\n') != len(line)-1 || w.stopped() {
		return errSpanWrite
	}
	select {
	case <-w.quit:
		return errSpanWrite
	case w.admit <- struct{}{}:
	default:
		return errSpanWrite
	}
	defer func() { <-w.admit }()
	// The timer and the worker use the same deadline. If the timer and the worker are both ready,
	// select can send the job. Then the worker sees the expired deadline and does not write.
	deadline := time.Now().Add(w.wait)
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	j := spanJob{line: slices.Clone(line), deadline: deadline, done: make(chan bool, 1)}
	select {
	case w.jobs <- j:
	case <-timer.C:
		return errSpanWrite
	case <-w.quit:
		return errSpanWrite
	}
	select {
	case ok := <-j.done:
		if ok {
			return nil
		}
	case <-timer.C:
	case <-w.quit:
	}
	return errSpanWrite
}

// stopped is true after close or after a failed Write.
func (w *spanWriter) stopped() bool { return w.state.Load()&(spanClosed|spanFailed) != 0 }

// close prevents all later claims, stops the worker after its current Write and releases the
// waiting callers. It never blocks, and it does not wait for a Write that blocks. It sets
// spanClosed before it closes quit, so each later write fails before admission.
func (w *spanWriter) close() {
	w.once.Do(func() {
		w.state.Or(spanClosed)
		close(w.quit)
	})
}
