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

package spendapi

import (
	"crypto/rand"
	"errors"
	"net/http"
	"slices"
	"sync"
	"testing"

	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

const (
	otherNamespace = "other"
	readerName     = "reader"
)

// viewerFixture has 3 Agents: 2 in the sample namespace and 1 in the other namespace.
// The token of the reader can list Agents only in the sample namespace.
type viewerFixture struct {
	fixture
	reader, admin, stranger string
	mu                      sync.Mutex
	reviews                 []authorizationv1.SubjectAccessReviewSpec
	accessErr               error
}

func newViewerFixture(t *testing.T, audiences ...string) *viewerFixture {
	t.Helper()
	f := &viewerFixture{fixture: newFixture(t, 3), reader: rand.Text(), admin: rand.Text(), stranger: rand.Text()}
	f.kube.(agentReader).items[2].Namespace = otherNamespace
	f.namespaces, f.tokens = []string{testNamespace, otherNamespace}, nil
	users := map[string]string{f.reader: readerName, f.admin: "admin", f.stranger: "stranger"}
	clients := fake.NewClientset()
	clients.PrependReactor("create", "tokenreviews", func(a k8stesting.Action) (bool, runtime.Object, error) {
		review := a.(k8stesting.CreateAction).GetObject().(*authenticationv1.TokenReview)
		if name, ok := users[review.Spec.Token]; ok {
			review.Status = authenticationv1.TokenReviewStatus{
				Authenticated: true, Audiences: []string{testAudience},
				User: authenticationv1.UserInfo{
					Username: name, UID: "uid-" + name, Groups: []string{"team-a"},
					Extra: map[string]authenticationv1.ExtraValue{"scope": {"view"}},
				},
			}
		} else {
			review.Status = authenticationv1.TokenReviewStatus{Error: "the token is not valid"}
		}
		review.Spec.Token = ""
		return true, review, nil
	})
	clients.PrependReactor("create", "subjectaccessreviews", func(a k8stesting.Action) (bool, runtime.Object, error) {
		review := a.(k8stesting.CreateAction).GetObject().(*authorizationv1.SubjectAccessReview)
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.accessErr != nil {
			return true, nil, f.accessErr
		}
		f.reviews = append(f.reviews, review.Spec)
		ns := review.Spec.ResourceAttributes.Namespace
		review.Status.Allowed = review.Spec.User == "admin" || (review.Spec.User == readerName && ns == testNamespace)
		return true, review, nil
	})
	f.viewers = &Viewers{
		Tokens: clients.AuthenticationV1().TokenReviews(), Access: clients.AuthorizationV1().SubjectAccessReviews(),
		Audiences: audiences,
	}
	f.serveGateway(t)
	return f
}

func namespacesOf(items []run) []string {
	result := make([]string, 0, len(items))
	for _, item := range items {
		result = append(result, item.Namespace)
	}
	return result
}

func TestViewerSeesOnlyPermittedNamespaces(t *testing.T) {
	f := newViewerFixture(t)
	p := decode[page[run]](t, f.call(t, http.MethodGet, runsPath, f.reader, http.StatusOK))
	if !slices.Equal(namespacesOf(p.Items), []string{testNamespace, testNamespace}) || p.NextCursor != "" {
		t.Fatalf("the reader sees runs in %v, want only %s", namespacesOf(p.Items), testNamespace)
	}
	g := decode[periodPage](t, f.call(t, http.MethodGet, testPeriod+"team", f.reader, http.StatusOK))
	if len(g.Items) != 2 || g.Items[0].LabelValue != firstAgent || g.Items[1].LabelValue != "agent-001" {
		t.Fatal("the period spend has a group from a namespace that the reader cannot list")
	}
	for _, spec := range f.reviews {
		r := spec.ResourceAttributes
		if spec.User != readerName || spec.UID != "uid-reader" || !slices.Equal(spec.Groups, []string{"team-a"}) ||
			!slices.Equal(spec.Extra["scope"], []string{"view"}) || r == nil || r.Verb != "list" ||
			r.Group != "core.koodam.dev" || r.Resource != "agents" || !slices.Contains(f.namespaces, r.Namespace) {
			t.Fatalf("the access review does not match the viewer: %+v", spec)
		}
	}
	if len(f.reviews) != 4 {
		t.Fatalf("got %d access reviews, want one for each namespace of each request", len(f.reviews))
	}
	f.call(t, http.MethodGet, specPath, f.reader, http.StatusOK)
	f.call(t, http.MethodHead, runsPath, f.reader, http.StatusOK)
}

func TestViewerPagesAcrossNamespaces(t *testing.T) {
	f := newViewerFixture(t)
	var seen []string
	next := runsPath + "?limit=1"
	for range 3 {
		p := decode[page[run]](t, f.call(t, http.MethodGet, next, f.admin, http.StatusOK))
		seen = append(seen, namespacesOf(p.Items)...)
		next = runsPath + "?limit=1&cursor=" + p.NextCursor
		if p.NextCursor == "" {
			break
		}
	}
	if !slices.Equal(seen, []string{testNamespace, testNamespace, otherNamespace}) {
		t.Fatalf("the admin pages give %v", seen)
	}
	p := decode[page[run]](t, f.call(t, http.MethodGet, runsPath+"?limit=2", f.admin, http.StatusOK))
	if len(p.Items) != 2 || p.NextCursor == "" {
		t.Fatal("a full page at the end of a namespace needs a cursor")
	}
	// A cursor for the other namespace is not valid for the reader.
	f.call(t, http.MethodGet, runsPath+"?cursor="+encodeCursor(runsPath+":"+testNamespace, otherNamespace+"/"),
		f.reader, http.StatusBadRequest)
	f.call(t, http.MethodGet, runsPath+"?cursor="+p.NextCursor, f.reader, http.StatusBadRequest)
}

func TestViewerRefusals(t *testing.T) {
	f := newViewerFixture(t)
	for _, token := range []string{rand.Text(), f.token, f.master} {
		w := f.call(t, http.MethodGet, runsPath, token, http.StatusUnauthorized)
		if w.Header().Get("WWW-Authenticate") != "Bearer" {
			t.Fatal("a 401 response needs the WWW-Authenticate header")
		}
	}
	f.call(t, http.MethodGet, runsPath, "", http.StatusUnauthorized)
	f.call(t, http.MethodGet, runsPath, f.stranger, http.StatusForbidden)
	f.call(t, http.MethodGet, specPath, f.stranger, http.StatusForbidden)

	f.accessErr = errors.New("the API server is not available")
	f.call(t, http.MethodGet, runsPath, f.reader, http.StatusBadGateway)
}

func TestViewerAudience(t *testing.T) {
	f := newViewerFixture(t, testAudience)
	f.call(t, http.MethodGet, runsPath, f.reader, http.StatusOK)
	f = newViewerFixture(t, "other-audience")
	f.call(t, http.MethodGet, runsPath, f.reader, http.StatusUnauthorized)
}

func TestNewRefusesBadViewers(t *testing.T) {
	clients := fake.NewClientset()
	good := Viewers{Tokens: clients.AuthenticationV1().TokenReviews(), Access: clients.AuthorizationV1().SubjectAccessReviews()}
	c := Config{
		Kube: agentReader{}, Namespaces: []string{testNamespace}, GatewayURL: testGateway,
		MasterKey: "master", Timeout: 1, Viewers: &good,
	}
	if _, err := New(c); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []Viewers{{Tokens: good.Tokens}, {Access: good.Access}, {good.Tokens, good.Access, []string{""}}} {
		c.Viewers = &bad
		if _, err := New(c); err == nil {
			t.Fatal("New accepted bad viewer reviews")
		}
	}
}
