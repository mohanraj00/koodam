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

package egress

import (
	"io"
	"sync"
	"sync/atomic"
	"time"
)

// activity records the time of the last byte of a call, in the 2 directions.
type activity struct{ last atomic.Int64 }

func (a *activity) touch() { a.last.Store(time.Now().UnixNano()) }

// watch calls fire once if no byte moves for idle. A blocked write also stops the reads, thus it
// ends the same way. The returned function stops the watch. When it returns, fire does not run,
// or it is complete.
func (a *activity) watch(idle time.Duration, fire func()) (stop func()) {
	a.touch()
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		t := time.NewTicker(idle / 4)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case now := <-t.C:
				if now.Sub(time.Unix(0, a.last.Load())) >= idle {
					fire()
					return
				}
			}
		}
	}()
	return sync.OnceFunc(func() {
		close(done)
		<-finished
	})
}

// activeReader records the activity of each read that moves bytes.
type activeReader struct {
	io.Reader
	a *activity
}

func (r *activeReader) Read(b []byte) (int, error) {
	n, err := r.Reader.Read(b)
	if n > 0 {
		r.a.touch()
	}
	return n, err
}

// activeBody is the upstream response body with activity records. For a 101 upgrade, the body
// is the upstream connection. Then the writes go to it too.
func activeBody(body io.ReadCloser, a *activity) io.ReadCloser {
	r := &activeReadCloser{activeReader: activeReader{Reader: body, a: a}, c: body}
	if w, ok := body.(io.Writer); ok {
		return &activeReadWriteCloser{activeReadCloser: r, w: w}
	}
	return r
}

type activeReadCloser struct {
	activeReader
	c io.Closer
}

func (r *activeReadCloser) Close() error { return r.c.Close() }

type activeReadWriteCloser struct {
	*activeReadCloser
	w io.Writer
}

func (rw *activeReadWriteCloser) Write(b []byte) (int, error) {
	n, err := rw.w.Write(b)
	if n > 0 {
		rw.a.touch()
	}
	return n, err
}
