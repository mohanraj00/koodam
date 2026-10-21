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

// Package workspace holds the files of the Workspace image that the Go code also uses. The build
// context of the image is this folder, so the one source of each file is here.
package workspace

import _ "embed"

// ProjectScript is koodam-project.sh. The Workspace image installs it as koodam-project.
// harness-install writes it into the harness folder of an Agent run.
//
//go:embed koodam-project.sh
var ProjectScript []byte
