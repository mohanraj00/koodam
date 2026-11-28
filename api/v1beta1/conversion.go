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

package v1beta1

// v1beta1 is the hub version of the API. Each other version converts to it and from it. The
// conversion code is in the package of the other version.

// Hub marks Agent as the hub version of its kind.
func (*Agent) Hub() {}

// Hub marks App as the hub version of its kind.
func (*App) Hub() {}

// Hub marks AppVersion as the hub version of its kind.
func (*AppVersion) Hub() {}

// Hub marks ApprovalRecord as the hub version of its kind.
func (*ApprovalRecord) Hub() {}

// Hub marks Hold as the hub version of its kind.
func (*Hold) Hub() {}

// Hub marks Profile as the hub version of its kind.
func (*Profile) Hub() {}

// Hub marks SpendBudget as the hub version of its kind.
func (*SpendBudget) Hub() {}

// Hub marks TenantQuota as the hub version of its kind.
func (*TenantQuota) Hub() {}

// Hub marks TestKey as the hub version of its kind.
func (*TestKey) Hub() {}

// Hub marks Workspace as the hub version of its kind.
func (*Workspace) Hub() {}
