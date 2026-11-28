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

package conversion

import (
	"crypto/tls"
	"net/http"

	"sigs.k8s.io/controller-runtime/pkg/webhook"
)

const (
	// ConvertPath is the path of the conversion handler. The CRDs name it.
	ConvertPath = "/convert"
	// ReadyPath is on the TLS port, so the pod has one port and the NetworkPolicy has one rule.
	ReadyPath = "/readyz"
)

// NewServer returns the controller-runtime webhook server with the conversion handler. It has no
// manager, no API client and no client CA. The server reads tls.crt and tls.key from certDir and
// loads them again when the files change. Thus a new certificate needs no restart.
func NewServer(host string, port int, certDir string, refused func(reason string)) webhook.Server {
	srv := webhook.NewServer(webhook.Options{
		Host:    host,
		Port:    port,
		CertDir: certDir,
		TLSOpts: []func(*tls.Config){func(c *tls.Config) { c.MinVersion = tls.VersionTLS12 }},
	})
	srv.Register(ConvertPath, NewHandler(refused))
	srv.Register(ReadyPath, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	return srv
}
