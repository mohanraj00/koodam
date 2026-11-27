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
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	authorizationv1 "k8s.io/api/authorization/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/mohanraj00/koodam/docs"
	"github.com/mohanraj00/koodam/internal/appclaim"
)

const (
	maxPage = 100
	// maxNamespaces limits the reviews and the reads of one request.
	maxNamespaces = 64
	runsPath      = "/v1/runs"
	spendPath     = "/v1/spend"
	specPath      = "/v1/openapi.json"
	// msgTimeLimit is the problem detail when the request time limit expires.
	msgTimeLimit = "The request time limit expired."
	// msgForbidden is the problem detail when the caller cannot list Agents in a target namespace.
	msgForbidden = "The caller cannot list Agents in a namespace of this server."
)

var errCursor = errors.New("the page cursor is not valid")

// Config sets the data scope, the gateway connection and the caller check.
// Configure one caller check: the issuer keys, the local tokens or the viewer reviews.
type Config struct {
	Kube       client.Reader
	Namespaces []string
	GatewayURL string
	MasterKey  string
	Timeout    time.Duration

	Claims       appclaim.Config
	Keys         *appclaim.KeySet
	StaticTokens []string
	Viewers      *Viewers
}

type api struct {
	kube       client.Reader
	namespaces []string
	viewers    *Viewers
	gateway    *liteLLM
	timeout    time.Duration
	claims     appclaim.Config
	keys       *appclaim.KeySet
	tokens     [][sha256.Size]byte
	// now gives the time for the period of a budget. If it is nil, the API uses time.Now.
	now func() time.Time
}

// New makes a read-only handler. One instance serves a fixed list of namespaces.
func New(c Config) (http.Handler, error) {
	if c.Kube == nil || !validNamespaces(c.Namespaces) || c.Timeout <= 0 {
		return nil, errors.New("a Kubernetes client, 1 to 64 different namespaces and a positive request time limit are necessary")
	}
	l, err := newLiteLLM(c.GatewayURL, c.MasterKey)
	if err != nil {
		return nil, err
	}
	checks := 0
	for _, set := range []bool{c.Keys != nil, len(c.StaticTokens) != 0, c.Viewers != nil} {
		if set {
			checks++
		}
	}
	if checks != 1 {
		return nil, errors.New("configure one of the issuer keys, the local tokens or the viewer reviews")
	}
	if c.Keys != nil {
		if err := c.Claims.Valid(); err != nil {
			return nil, err
		}
	}
	if c.Viewers != nil && !c.Viewers.valid() {
		return nil, errors.New("the viewer reviews need a TokenReview client, a SubjectAccessReview client and no empty audience")
	}
	a := &api{
		kube: c.Kube, namespaces: slices.Clone(c.Namespaces), viewers: c.Viewers,
		gateway: l, timeout: c.Timeout, claims: c.Claims, keys: c.Keys,
	}
	a.tokens = make([][sha256.Size]byte, 0, len(c.StaticTokens))
	for _, token := range c.StaticTokens {
		if token == "" || strings.ContainsAny(token, " \r\n\t") {
			return nil, errors.New("a local token is empty or contains white space")
		}
		a.tokens = append(a.tokens, sha256.Sum256([]byte(token)))
	}
	return a, nil
}

func validNamespaces(namespaces []string) bool {
	if len(namespaces) == 0 || len(namespaces) > maxNamespaces {
		return false
	}
	for i, namespace := range namespaces {
		if len(validation.IsDNS1123Label(namespace)) != 0 || slices.Contains(namespaces[:i], namespace) {
			return false
		}
	}
	return true
}

// bearer uses the same header rules as the App claim check.
func bearer(r *http.Request) (string, bool) {
	headers := r.Header.Values("Authorization")
	if len(headers) != 1 {
		return "", false
	}
	scheme, token, ok := strings.Cut(headers[0], " ")
	return token, ok && token != "" && strings.EqualFold(scheme, "Bearer")
}

// scope gives the namespaces of targets that the caller can read, or the HTTP status of the refusal.
func (a *api) scope(ctx context.Context, r *http.Request, targets []string,
	checks []authorizationv1.ResourceAttributes) ([]string, int, problem) {
	token, ok := bearer(r)
	if !ok || (a.viewers == nil && !a.authorized(token)) {
		return nil, http.StatusUnauthorized, problem{"A valid bearer token is necessary."}
	}
	if a.viewers == nil {
		if len(targets) == 0 {
			return nil, http.StatusForbidden, problem{msgForbidden}
		}
		return targets, http.StatusOK, problem{}
	}
	user, err := a.viewers.user(ctx, token)
	if errors.Is(err, errNotAuthenticated) {
		return nil, http.StatusUnauthorized, problem{"A valid bearer token is necessary."}
	}
	var namespaces []string
	if err == nil {
		namespaces, err = a.viewers.allowed(ctx, user, targets, checks)
	}
	switch {
	case ctx.Err() != nil:
		return nil, http.StatusGatewayTimeout, problem{msgTimeLimit}
	case err != nil:
		return nil, http.StatusBadGateway, problem{"The Kubernetes access review is not available."}
	case len(namespaces) == 0:
		return nil, http.StatusForbidden, problem{msgForbidden}
	}
	return namespaces, http.StatusOK, problem{}
}

func (a *api) authorized(token string) bool {
	if a.keys != nil {
		_, err := a.claims.Verify(a.keys, token, time.Now())
		return err == nil
	}
	sum := sha256.Sum256([]byte(token))
	var match int
	for _, want := range a.tokens {
		match |= subtle.ConstantTimeCompare(sum[:], want[:])
	}
	return match == 1
}

func (a *api) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), a.timeout)
	defer cancel()
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		reply(w, r, http.StatusMethodNotAllowed, problem{"Use GET or HEAD."})
		return
	}
	// A run path reads one namespace, so the server checks the access to that namespace only.
	targets, checks := a.namespaces, []authorizationv1.ResourceAttributes{listAgents}
	ref, isRun := parseRun(r.URL.Path)
	if isRun {
		targets = nil
		if slices.Contains(a.namespaces, ref.namespace) {
			targets = []string{ref.namespace}
		}
	}
	namespaces, status, refusal := a.scope(ctx, r, targets, checks)
	if status != http.StatusOK {
		if status == http.StatusUnauthorized {
			w.Header().Set("WWW-Authenticate", "Bearer")
		}
		reply(w, r, status, refusal)
		return
	}
	switch path := r.URL.Path; {
	case isRun:
		a.serveRun(ctx, w, r, ref)
	case path == specPath:
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(docs.SpendOpenAPI))
		} else {
			w.WriteHeader(http.StatusOK)
		}
	case path == runsPath || path == spendPath:
		a.list(ctx, w, r, namespaces)
	case path == budgetsPath:
		a.listBudgets(ctx, w, r, namespaces)
	default:
		reply(w, r, http.StatusNotFound, problem{"This path does not exist."})
	}
}

type problem struct {
	Error string `json:"error"`
}

func reply(w http.ResponseWriter, r *http.Request, status int, body any) {
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		_ = json.NewEncoder(w).Encode(body)
	}
}

func (a *api) list(ctx context.Context, w http.ResponseWriter, r *http.Request, namespaces []string) {
	q, err := url.ParseQuery(r.URL.RawQuery)
	limit, valid := pageLimit(q)
	if err != nil || !valid {
		reply(w, r, http.StatusBadRequest, problem{"Use a valid query and a limit from 1 to 100."})
		return
	}
	var result any
	if r.URL.Path == runsPath {
		result, err = a.runs(ctx, namespaces, limit, q.Get("cursor"))
	} else {
		p, ok := parsePeriod(q)
		if !ok {
			reply(w, r, http.StatusBadRequest, problem{"Use UTC dates and groupBy=label:<name> or groupBy=namespace."})
			return
		}
		result, err = a.spend(ctx, namespaces, p, limit, q.Get("cursor"))
	}
	switch {
	case ctx.Err() != nil:
		reply(w, r, http.StatusGatewayTimeout, problem{msgTimeLimit})
	case err == nil:
		reply(w, r, http.StatusOK, result)
	case errors.Is(err, errCursor) || apierrors.IsBadRequest(err):
		reply(w, r, http.StatusBadRequest, problem{"The page cursor is not valid."})
	case apierrors.IsResourceExpired(err):
		reply(w, r, http.StatusGone, problem{"The page cursor expired. Start again with no cursor."})
	default:
		reply(w, r, http.StatusBadGateway, problem{"The spend data is not available."})
	}
}

// listBudgets has no query and no pages, because it has one item for each namespace at most.
func (a *api) listBudgets(ctx context.Context, w http.ResponseWriter, r *http.Request, namespaces []string) {
	if r.URL.RawQuery != "" {
		reply(w, r, http.StatusBadRequest, problem{"This path has no query."})
		return
	}
	result, err := a.budgets(ctx, namespaces)
	switch {
	case ctx.Err() != nil:
		reply(w, r, http.StatusGatewayTimeout, problem{msgTimeLimit})
	case err != nil:
		reply(w, r, http.StatusBadGateway, problem{"The budget data is not available."})
	default:
		reply(w, r, http.StatusOK, result)
	}
}

func pageLimit(q url.Values) (int, bool) {
	for _, values := range q {
		if len(values) != 1 {
			return 0, false
		}
	}
	if !q.Has("limit") {
		return maxPage, true
	}
	n, err := strconv.Atoi(q.Get("limit"))
	return n, err == nil && n > 0 && n <= maxPage
}

// A cursor keeps a position with the query that gave it to the caller.
func encodeCursor(scope, position string) string {
	b, _ := json.Marshal([2]string{scope, position})
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeCursor(scope, cursor string) (string, error) {
	if cursor == "" {
		return "", nil
	}
	var parts []string
	b, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil || json.Unmarshal(b, &parts) != nil || len(parts) != 2 || parts[0] != scope {
		return "", errCursor
	}
	return parts[1], nil
}
