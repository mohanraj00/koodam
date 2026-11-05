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
	"context"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/mohanraj00/koodam/internal/agentingress"
)

// The fixtures have only public fake names, UIDs, ports, images and status. They have no env,
// token, Secret, key alias or key hash.
const (
	tgNamespace      = "runs"
	tgOtherNamespace = "runs-b"
	tgPolicyName     = "koodam-agent"
	tgProxyNamespace = "koodam-system"
	tgRelease        = "koodam"
	tgOther          = "other"
	tgNameKey        = "app.kubernetes.io/name"
	tgInstanceKey    = "app.kubernetes.io/instance"
	tgComponentKey   = "app.kubernetes.io/component"
)

// The fixed name of a fake read. A failure message never contains an object, an error or an
// address.
const (
	opOther = "unexpected read"
)

// targetReader is a local fake client.Reader. It keeps the fixed names of the reads only in memory
// in this process. It has no write method and no object: it records each read and refuses it. The
// constructor tests use it to prove that the constructor reads no object.
type targetReader struct {
	ctx context.Context // Each read must get this context.
	ops []string
}

// newTargetWorld returns a fake with no object.
func newTargetWorld(ctx context.Context) *targetReader {
	return &targetReader{ctx: ctx}
}

func (f *targetReader) Get(context.Context, client.ObjectKey, client.Object, ...client.GetOption) error {
	f.ops = append(f.ops, opOther)
	return apierrors.NewNotFound(schema.GroupResource{}, "")
}

func (f *targetReader) List(context.Context, client.ObjectList, ...client.ListOption) error {
	f.ops = append(f.ops, opOther)
	return apierrors.NewBadRequest("")
}

var _ client.Reader = (*targetReader)(nil)

func tgProxyLabels() map[string]string {
	return map[string]string{tgNameKey: tgRelease, tgInstanceKey: tgRelease,
		tgComponentKey: agentingress.ProxyComponent}
}

func tgGuard(namespace string) agentingress.PolicyGuard {
	return agentingress.PolicyGuard{Namespace: namespace, Name: tgPolicyName, ProxyNamespace: tgProxyNamespace,
		ProxyLabels: tgProxyLabels()}
}

// tgResolver returns a resolver of f with the guards of the two run namespaces.
func tgResolver(t *testing.T, f *targetReader) *targetResolver {
	t.Helper()
	r, err := newTargetResolver(f, []agentingress.PolicyGuard{tgGuard(tgNamespace), tgGuard(tgOtherNamespace)})
	if err != nil || r == nil {
		t.Fatal("newTargetResolver refused a valid configuration")
	}
	return r
}
