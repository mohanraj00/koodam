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
	"net/url"
	"strings"

	apivalidation "k8s.io/apimachinery/pkg/api/validation"
)

// routePrefix is the first path segment of each ingress route.
const routePrefix = "run"

// route is the parsed form of /run/<namespace>/<agent>/<path>. namespace and agent are decoded
// names. upstream is a relative URL with only the backend path and the query. It has no scheme,
// host or user, thus the caller cannot select a target. upstream holds private path and query
// data. Do not log, print or serialize it, and do not put it in an error.
type route struct {
	namespace string
	agent     string
	upstream  url.URL
}

// parseRoute returns the route of u. u must be in origin form: a path and an optional query only.
// parseRoute splits URL.EscapedPath at each slash and decodes each segment one time. It does not
// clean the path. It refuses a RawPath that does not agree with Path, a malformed escape, an empty
// segment, a decoded slash, a dot or dot-dot segment and a name that is not valid. The backend path
// must have one or more segments. upstream keeps the decoded backend path, its escaped bytes and
// the query bytes. parseRoute does not parse the query. On a refusal, it returns an empty route
// and ErrRoute. It does not change u.
func parseRoute(u *url.URL) (route, error) {
	if u == nil || u.Scheme != "" || u.Opaque != "" || u.User != nil || u.Host != "" ||
		u.Fragment != "" || u.RawFragment != "" {
		return route{}, ErrRoute
	}
	// EscapedPath ignores a RawPath that is not valid or that does not agree with Path. Refuse
	// this fallback, thus the segments are always the bytes that the caller sent.
	escaped := u.EscapedPath()
	if u.RawPath != "" && escaped != u.RawPath {
		return route{}, ErrRoute
	}
	rest, ok := strings.CutPrefix(escaped, "/")
	if !ok {
		return route{}, ErrRoute
	}
	raw := strings.Split(rest, "/")
	if len(raw) < 4 {
		return route{}, ErrRoute
	}
	decoded := make([]string, len(raw))
	for i, s := range raw {
		d, err := url.PathUnescape(s)
		if err != nil || d == "" || d == "." || d == ".." || strings.Contains(d, "/") {
			return route{}, ErrRoute
		}
		decoded[i] = d
	}
	namespace, agent := decoded[1], decoded[2]
	if decoded[0] != routePrefix || len(apivalidation.ValidateNamespaceName(namespace, false)) > 0 ||
		len(apivalidation.NameIsDNSSubdomain(agent, false)) > 0 {
		return route{}, ErrRoute
	}
	return route{namespace: namespace, agent: agent, upstream: url.URL{
		Path:       "/" + strings.Join(decoded[3:], "/"),
		RawPath:    "/" + strings.Join(raw[3:], "/"),
		RawQuery:   u.RawQuery,
		ForceQuery: u.ForceQuery,
	}}, nil
}
