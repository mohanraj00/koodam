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

// Package ingress has the caller checks of the traced Agent ingress proxy of #147. It does not
// register a handler, open a listener or forward a request.
package ingress

import (
	"errors"
	"net/http"
	"strings"

	authenticationv1 "k8s.io/api/authentication/v1"
	apivalidation "k8s.io/apimachinery/pkg/api/validation"
)

// ErrAuthentication is the error for each authentication failure. Its text is fixed. It never
// contains a credential, a header, an identity or a review status.
var ErrAuthentication = errors.New("the ingress caller is not authenticated")

// Caller is the identity of a caller that TokenReview proved. It has only the ServiceAccount
// namespace, name and UID.
type Caller struct {
	Namespace string
	Name      string
	UID       string
}

// authorizationHeader is the name of the request header with the credential. A header name does
// not have a case.
const authorizationHeader = "Authorization"

// serviceAccountPrefix is the start of a ServiceAccount user name in a TokenReview status.
const serviceAccountPrefix = "system:serviceaccount:"

// bearer returns the one Bearer credential of r. It returns ErrAuthentication if r is nil, or if r
// did not come on TLS. A forwarded protocol header cannot replace r.TLS. The header map must have
// exactly one key that matches Authorization in any case, and that key must have exactly one value.
// A second matching key refuses, also if it has no value. The value must be the scheme Bearer, one
// space and one token in the RFC 6750 token syntax. The scheme does not have a case. This refuses
// whitespace, control characters, a comma list and a second credential. The function does not
// change r.
func bearer(r *http.Request) (string, error) {
	if r == nil || r.TLS == nil {
		return "", ErrAuthentication
	}
	keys := 0
	var values []string
	for k, vs := range r.Header {
		if strings.EqualFold(k, authorizationHeader) {
			keys++
			values = append(values, vs...)
		}
	}
	if keys != 1 || len(values) != 1 {
		return "", ErrAuthentication
	}
	scheme, token, ok := strings.Cut(values[0], " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || !tokenSyntax(token) {
		return "", ErrAuthentication
	}
	return token, nil
}

// tokenSyntax is true if s is a b64token of RFC 6750: 1 or more letters, digits, "-", ".", "_",
// "~", "+" or "/", then 0 or more "=". It is false for all other bytes, also for spaces and
// control characters.
func tokenSyntax(s string) bool {
	body := strings.TrimRight(s, "=")
	if body == "" {
		return false
	}
	for i := range len(body) {
		c := body[i]
		switch {
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9':
		case c == '-', c == '.', c == '_', c == '~', c == '+', c == '/':
		default:
			return false
		}
	}
	return true
}

// callerFrom returns the Caller of a TokenReview user. The user name must be
// system:serviceaccount:<namespace>:<name>, with exactly 4 parts, a valid namespace name and a
// valid ServiceAccount name. The UID must not be empty. It does not decode the token, and it does not
// read a header. If one check fails, it returns ErrAuthentication and an empty Caller.
func callerFrom(u authenticationv1.UserInfo) (Caller, error) {
	parts := strings.Split(u.Username, ":")
	if len(parts) != 4 || !strings.HasPrefix(u.Username, serviceAccountPrefix) || u.UID == "" {
		return Caller{}, ErrAuthentication
	}
	namespace, name := parts[2], parts[3]
	if len(apivalidation.ValidateNamespaceName(namespace, false)) > 0 ||
		len(apivalidation.NameIsDNSSubdomain(name, false)) > 0 {
		return Caller{}, ErrAuthentication
	}
	return Caller{Namespace: namespace, Name: name, UID: u.UID}, nil
}
