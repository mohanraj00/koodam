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
	"slices"
	"strconv"
	"strings"
	"testing"
)

// The route tests use only the non-credential canary as private path, query and fragment data. A
// failure message has fixed text. It does not print a URL, a route or an error.

// routeTestPath is a valid route request URI for team-a/web with backend path /x.
const routeTestPath = "/run/team-a/web/x"

// parsedURI returns the request URI s as the HTTP server parses it.
func parsedURI(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.ParseRequestURI(s)
	if err != nil {
		t.Fatal("a test request URI did not parse")
	}
	return u
}

// parseUnchanged returns parseRoute(u). It fails the test if parseRoute changed u.
func parseUnchanged(t *testing.T, u *url.URL) (route, error) {
	t.Helper()
	var before url.URL
	if u != nil {
		before = *u
	}
	got, err := parseRoute(u)
	if u != nil && *u != before {
		t.Fatal("parseRoute changed the URL")
	}
	return got, err
}

// refusedRoute is true if parseRoute refused with only ErrRoute, an empty route and no private
// detail.
func refusedRoute(got route, err error) bool {
	return err == ErrRoute && got == (route{}) && !strings.Contains(err.Error(), canary)
}

func TestParseRoute(t *testing.T) {
	x := url.URL{Path: "/x", RawPath: "/x"}
	query := "a=1&a=2&b=c+d&e=%41%2F&f=%2f&q=" + canary
	for _, tc := range []struct {
		name, uri, namespace, agent string
		want                        url.URL
	}{
		{"simple", routeTestPath, testNamespace, testAgent, x},
		{"nested", "/run/team-a/web/api/v1/items", testNamespace, testAgent,
			url.URL{Path: "/api/v1/items", RawPath: "/api/v1/items"}},
		{"dotted agent", "/run/team-a/web.v1/x", testNamespace, "web.v1", x},
		{"longest names", "/run/" + strings.Repeat("a", 63) + "/" + strings.Repeat("b", 253) + "/x",
			strings.Repeat("a", 63), strings.Repeat("b", 253), x},
		{"escaped names", "/%72un/team%2Da/w%65b/x", testNamespace, testAgent, x},
		{"escaped backend", "/run/team-a/web/a%20b/%e2%9c%93/100%25/%41+/a%252Fb", testNamespace, testAgent,
			url.URL{Path: "/a b/✓/100%/A+/a%2Fb", RawPath: "/a%20b/%e2%9c%93/100%25/%41+/a%252Fb"}},
		{"query", "/run/team-a/web/" + canary + "?" + query, testNamespace, testAgent,
			url.URL{Path: "/" + canary, RawPath: "/" + canary, RawQuery: query}},
		{"empty query", "/run/team-a/web/x?", testNamespace, testAgent,
			url.URL{Path: "/x", RawPath: "/x", ForceQuery: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseUnchanged(t, parsedURI(t, tc.uri))
			if err != nil || got != (route{tc.namespace, tc.agent, tc.want}) {
				t.Fatal("parseRoute did not return the exact route")
			}
			u := got.upstream
			if u.IsAbs() || u.Scheme != "" || u.Host != "" || u.User != nil || u.Opaque != "" {
				t.Fatal("the upstream URL has a target authority")
			}
			if u.EscapedPath() != u.RawPath {
				t.Fatal("the upstream URL did not keep the escaped backend path")
			}
		})
	}
}

func TestParseRouteRefusesPaths(t *testing.T) {
	initial := []string{
		// Too few segments, and empty segments.
		"/", "/run", "/run/team-a", "/run/team-a/web", "/run/team-a/web/", "/run/team-a/web/x/",
		"/run/team-a/web/" + canary + "/?q=" + canary, "//run/team-a/web/x", "/run//team-a/web/x",
		"/run/team-a//web/x", "/run/team-a/web//x", "/run/team-a/web/x//y",
		// Wrong prefix. The prefix is decoded one time only.
		"/runs/team-a/web/x", "/Run/team-a/web/x", "/api/run/team-a/web/x", "/r%75n%20/team-a/web/x",
		"/%2572un/team-a/web/x",
		// Namespace names that are not valid.
		"/run/Team-A/web/x", "/run/team.a/web/x", "/run/team_a/web/x", "/run/-a/web/x", "/run/team%41/web/x",
		"/run/team%252Da/web/x", "/run/t%C3%A9am/web/x", "/run/" + strings.Repeat("a", 64) + "/web/x",
		// Agent names that are not valid.
		"/run/team-a/Web/x", "/run/team-a/w_b/x", "/run/team-a/-web/x", "/run/team-a/w%20b/x",
		"/run/team-a/" + strings.Repeat("b", 254) + "/x",
		// A caller target authority.
		"https://evil.test/run/team-a/web/x", "http://evil.test:8080/run/team-a/web/x",
		"//evil.test/run/team-a/web/x",
	}
	// Put each raw or encoded dot, dot-dot and slash form in each segment.
	segments := []string{routePrefix, testNamespace, testAgent, "x", "y"}
	bad := []string{".", "..", "%2e", "%2E", "%2e%2e", "%2E%2E", ".%2e", "%2E.", "a%2Fb", "a%2fb", "%2F", "%2f"}
	uris := make([]string, 0, len(initial)+len(bad)*len(segments))
	uris = append(uris, initial...)
	for i := range segments {
		for _, s := range bad {
			c := slices.Clone(segments)
			c[i] = s
			uris = append(uris, "/"+strings.Join(c, "/"))
		}
	}
	for i, s := range uris {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			if got, err := parseUnchanged(t, parsedURI(t, s)); !refusedRoute(got, err) {
				t.Fatal("parseRoute did not refuse a malformed route with only ErrRoute")
			}
		})
	}
}

// TestParseRouteRefusesURLs uses URLs that the request parser does not make. They prove the
// RawPath fallback check and the origin-form check.
func TestParseRouteRefusesURLs(t *testing.T) {
	for _, tc := range []struct {
		name string
		u    *url.URL
	}{
		{"nil", nil},
		{"empty", &url.URL{}},
		{"no leading slash", &url.URL{Path: "run/team-a/web/x"}},
		{"asterisk", &url.URL{Path: "*"}},
		{"malformed escape", &url.URL{Path: routeTestPath, RawPath: "/run/team-a/web/%zz"}},
		{"short escape", &url.URL{Path: routeTestPath, RawPath: "/run/team-a/web/x%2"}},
		{"other raw path", &url.URL{Path: routeTestPath, RawPath: "/run/team-a/web/y"}},
		{"raw path not escaped", &url.URL{Path: litRouteSpacePath, RawPath: litRouteSpacePath}},
		{"raw path with slash", &url.URL{Path: "/run/team-a/web/a/b", RawPath: "/run/team-a/web/a%2Fb"}},
		{"raw path with dot dot", &url.URL{Path: "/run/team-a/web/../x", RawPath: "/run/team-a/web/%2E%2E/x"}},
		{"scheme", &url.URL{Scheme: "https", Path: routeTestPath}},
		{"host", &url.URL{Host: "evil.test", Path: routeTestPath}},
		{"host and port", &url.URL{Host: "evil.test:8080", Path: routeTestPath}},
		{"user", &url.URL{User: url.User(opaque()), Path: routeTestPath}},
		{"user and password", &url.URL{User: url.UserPassword(opaque(), opaque()), Path: routeTestPath}},
		{"opaque", &url.URL{Opaque: routeTestPath}},
		{"fragment", &url.URL{Path: routeTestPath, Fragment: canary}},
		{"raw fragment", &url.URL{Path: routeTestPath, RawFragment: canary}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := parseUnchanged(t, tc.u); !refusedRoute(got, err) {
				t.Fatal("parseRoute did not refuse the URL with only ErrRoute")
			}
		})
	}
	// A RawPath that agrees with Path is not a fallback.
	u := &url.URL{Path: litRouteSpacePath, RawPath: "/run/team-a/web/a%20b"}
	got, err := parseUnchanged(t, u)
	if err != nil || got != (route{testNamespace, testAgent, url.URL{Path: "/a b", RawPath: "/a%20b"}}) {
		t.Fatal("parseRoute refused a RawPath that agrees with Path")
	}
}

const (
	litRouteSpacePath = "/run/team-a/web/a b"
)
