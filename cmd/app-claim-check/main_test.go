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

import "testing"

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
