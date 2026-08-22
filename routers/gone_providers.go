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

// goneProviders is the providers family's retirement table: each address visor
// no longer serves, and the address that replaced it. See gone.go for how it is
// answered.
//
// All five collapse onto ONE resource. Which of the five you meant is the method
// you send, and which provider you meant is the (owner, name) pair that IS its
// primary key — the same `owner/name` string the retired ?id carried, moved out
// of the query into the path.
//
// The item successor is written with its parameters as the document spells them,
// so a caller reading the Link header sees the shape it has to fill in rather
// than one org's row.
var goneProviders = map[string][]string{
	"/v1/get-providers":   {"/v1/providers"},
	"/v1/add-provider":    {"/v1/providers"},
	"/v1/get-provider":    {"/v1/providers/{owner}/{name}"},
	"/v1/update-provider": {"/v1/providers/{owner}/{name}"},
	"/v1/delete-provider": {"/v1/providers/{owner}/{name}"},
}
