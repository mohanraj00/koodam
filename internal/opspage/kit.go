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

// Package opspage has the operations page of the spend API and the pinned mugham kit files.
package opspage

import (
	"embed"
)

// files holds the page and the vendored mugham kit files under static/kit. mugham.pin.json
// gives the release and the SHA-256 value of each kit file.
//
//go:embed static
var files embed.FS

// Pin is the content of mugham.pin.json.
//
//go:embed mugham.pin.json
var Pin []byte
