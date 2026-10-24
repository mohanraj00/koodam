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
	"net"
	"sync"
)

// MaxPerSource is the limit of the open connections from one source IP on a proxy listener.
const MaxPerSource = 64

// SourceLimit returns a listener that closes a new connection at once if its source IP has max
// open connections.
func SourceLimit(ln net.Listener, max int) net.Listener {
	return &sourceListener{Listener: ln, max: max, open: map[string]int{}}
}

type sourceListener struct {
	net.Listener
	max  int
	mu   sync.Mutex
	open map[string]int
}

func (l *sourceListener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		src := sourceIP(c.RemoteAddr().String())
		l.mu.Lock()
		ok := l.open[src] < l.max
		if ok {
			l.open[src]++
		}
		l.mu.Unlock()
		if !ok {
			_ = c.Close()
			continue
		}
		return &sourceConn{Conn: c, release: sync.OnceFunc(func() {
			l.mu.Lock()
			if l.open[src]--; l.open[src] == 0 {
				delete(l.open, src)
			}
			l.mu.Unlock()
		})}, nil
	}
}

// sourceConn gives its place back to the listener on the first Close.
type sourceConn struct {
	net.Conn
	release func()
}

func (c *sourceConn) Close() error {
	c.release()
	return c.Conn.Close()
}
