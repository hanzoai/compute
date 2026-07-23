// Copyright 2024 Hanzo Industries Inc. All Rights Reserved.
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

// Paginate is the ONE offset/total helper the list handlers use, replacing
// Beego's pagination.SetPaginator: given the 1-based page string, the page size
// and the total row count, it returns the SQL offset and the total (echoed back
// to the client as data2 for its own page math). page < 1 clamps to the first
// page, matching Beego's Paginator.Page() lower bound.
func Paginate(page string, perPage int, total int64) (offset int, nums int64) {
	p := ParseInt(page)
	if p < 1 {
		p = 1
	}
	if perPage < 0 {
		perPage = 0
	}
	return (p - 1) * perPage, total
}
