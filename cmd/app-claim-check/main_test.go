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
	"context"
	"errors"
	"testing"
	"time"
)

func TestRoutes(t *testing.T) {
	var r routes
	if r.Set("18080=8080") != nil || r.Set("18081=8081") != nil || len(r) != 2 || r[1] != [2]int{18081, 8081} {
		t.Errorf("valid routes: %v", r)
	}
	for _, v := range []string{"", "8080", "a=b", "0=8080", "8080=70000", "8080=", "=8080"} {
		if r.Set(v) == nil {
			t.Errorf("%q is accepted", v)
		}
	}
	if len(r) != 2 || r.String() == "" {
		t.Errorf("A route that is not valid changed the list: %v", r)
	}
}

// every calls the load function again after an error, and it stops with the context.
func TestEvery(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := make(chan struct{}, 8)
	done := make(chan struct{})
	go func() {
		every(ctx, time.Millisecond, "test:", func() error {
			select {
			case calls <- struct{}{}:
			default:
			}
			return errors.New("the load failed")
		})
		close(done)
	}()
	for range 2 {
		select {
		case <-calls:
		case <-time.After(5 * time.Second):
			t.Fatal("every did not call the load function 2 times")
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("every did not stop with the context")
	}
}
