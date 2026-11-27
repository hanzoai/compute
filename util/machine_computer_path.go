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

package util

import "strings"

// IsMachineComputerPath reports exactly /v1/machines/<owner>/<name>/computer.
//
// Three gates use this one shape — the authorizer, the audit redactor and the
// raw response writer — and each has to agree on the SAME address. A suffix
// match would also catch a longer, unrelated path, so the segment count and
// every literal are checked.
func IsMachineComputerPath(path string) bool {
	seg := strings.Split(strings.Trim(path, "/"), "/")
	return len(seg) == 5 && seg[0] == "v1" && seg[1] == "machines" &&
		seg[2] != "" && seg[3] != "" && seg[4] == "computer"
}
