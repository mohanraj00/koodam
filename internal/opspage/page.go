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

package opspage

import (
	"bytes"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

// Policy is the Content-Security-Policy of the page. The page has no inline script and no
// inline style. It reads data only from its own origin.
const Policy = "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; " +
	"img-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

var contentTypes = map[string]string{
	".html": "text/html; charset=utf-8",
	".js":   "text/javascript; charset=utf-8",
	".css":  "text/css; charset=utf-8",
	".json": "application/json",
	"":      "text/plain; charset=utf-8",
}

// Handler serves the page at / and its files. It serves only the embedded files, with no
// directory list. The page reads the spend API of the same origin with the token of the viewer.
func Handler() http.Handler {
	served := map[string][]byte{}
	err := fs.WalkDir(files, "static", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if _, ok := contentTypes[path.Ext(name)]; !ok {
			return fs.ErrInvalid
		}
		data, err := files.ReadFile(name)
		served[strings.TrimPrefix(name, "static")] = data
		return err
	})
	if err != nil {
		panic("opspage: the embedded files are not valid: " + err.Error())
	}
	served["/"] = served["/index.html"]
	delete(served, "/index.html")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", Policy)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		h.Set("Cache-Control", "no-cache")
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			h.Set("Allow", "GET, HEAD")
			http.Error(w, "Use GET or HEAD.", http.StatusMethodNotAllowed)
			return
		}
		data, ok := served[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		name := r.URL.Path
		if name == "/" {
			name = "/index.html"
		}
		h.Set("Content-Type", contentTypes[path.Ext(name)])
		http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(data))
	})
}
