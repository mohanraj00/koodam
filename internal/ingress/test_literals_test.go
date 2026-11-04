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

// The span method values of the contract.
const (
	wantSpanGET   = "GET"
	wantSpanPOST  = "POST"
	wantSpanOther = "OTHER"
)

const (
	// litLowerGet is the lower case text get: an RBAC verb, or a method name that is not canonical.
	litLowerGet = "get"
)

// wantSpanKind is the span kind value of the contract.
const wantSpanKind = "ingress"

// The span field names that the tests read or expect.
const (
	litSpanMethodKey = "method"
	litSpanStatusKey = "status"
)
