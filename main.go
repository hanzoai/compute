// Copyright 2023 Hanzo Industries Inc. All Rights Reserved.
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

package main

import (
	"github.com/beego/beego"

	"github.com/hanzoai/visor/pkg/visor"
)

func main() {
	// visor.Bootstrap is the single in-process boot path — DB, authz, parsers,
	// filters, session config and background tickers. It is shared verbatim with
	// the embedded cloud mount (pkg/visor.Handler), so standalone and fused never
	// drift. Here main() owns the listener; beego.Run binds it and blocks.
	visor.Bootstrap()
	beego.Run()
}
