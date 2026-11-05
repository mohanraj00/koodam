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

type spanJob struct {
	line []byte
	done chan bool // capacity 1, so the worker never blocks on it
}

// spanWriter writes span lines to 1 io.Writer, for example os.Stdout. 1 worker goroutine, started
// once, does all writes in sequence with 1 Write call for each line. A caller waits at most wait.
// No line waits in a queue: a caller that cannot give its line to the worker in time gets
// errSpanWrite, and the line is dropped. After a failed, short or panicked Write, the writer
// refuses all later lines, because the output can hold a partial line.
//
// Go cannot stop a Write call that blocks. Then the worker and its 1 line stay until Write
// returns. Other callers get errSpanWrite in the wait limit. close does not wait for the worker.
// After close, write refuses each line before admission, and the worker starts no new Write.
type spanWriter struct {
	out    io.Writer
	wait   time.Duration
	admit  chan struct{}
	jobs   chan spanJob
	quit   chan struct{}
	once   sync.Once
	closed atomic.Bool
	failed atomic.Bool
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
			j.done <- w.writeOne(j.line)
		}
	}
}

// writeOne makes at most 1 Write call. A line that the worker takes after close is not written.
func (w *spanWriter) writeOne(line []byte) (ok bool) {
	if w.stopped() {
		return false
	}
	defer func() {
		if recover() != nil || !ok {
			w.failed.Store(true)
		}
	}()
	n, err := w.out.Write(line)
	return err == nil && n == len(line)
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
	timer := time.NewTimer(w.wait)
	defer timer.Stop()
	j := spanJob{line: slices.Clone(line), done: make(chan bool, 1)}
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
func (w *spanWriter) stopped() bool { return w.closed.Load() || w.failed.Load() }

// close stops the worker after its current Write and releases the waiting callers. It never
// blocks. It sets the closed state before it closes quit, so each later write fails before
// admission.
func (w *spanWriter) close() {
	w.once.Do(func() {
		w.closed.Store(true)
		close(w.quit)
	})
}
