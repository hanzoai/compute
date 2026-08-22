// Copyright 2026 Hanzo Industries Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package routers

import "github.com/hanzoai/visor/pkg/gone"

// goneWhitelabel is the whitelabel's retirement table. One address, one verb,
// and the verb was the only thing in it.
//
// The static policy admits this address anonymously alongside its successor
// (authz/authz.go), which it has to: the console reads its branding before a
// visitor has signed in, so a 403 here would hide the retirement from exactly
// the caller that has to act on it.
var goneWhitelabel = gone.Table{
	"/v1/get-whitelabel": "/v1/whitelabel",
}
