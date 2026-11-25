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
	"encoding/base64"
	"encoding/json"
	"errors"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	maxPage  = 100
	runsPath = "/v1/runs"
)

var errCursor = errors.New("the page cursor is not valid")

type api struct {
	kube      client.Reader
	namespace string
	gateway   *liteLLM
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
