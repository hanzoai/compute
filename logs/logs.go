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

// Package logs is visor's ONE leveled logger — a thin printf-style facade over
// the ZAP-native logger (github.com/luxfi/log), the same logger zip writes
// through. It exists so every call site logs one way: the printf spelling the
// service already used, now backed by luxfi/log instead of Beego's logs. Import
// this in place of github.com/beego/beego/logs; the Info/Warning/Error call
// sites are unchanged.
package logs

import (
	"fmt"

	log "github.com/luxfi/log"
)

// render applies printf formatting only when args are present, so a lone format
// string carrying a literal '%' is never mangled into a "%!"(NOVERB) rendering.
func render(f string, v []interface{}) string {
	if len(v) == 0 {
		return f
	}
	return fmt.Sprintf(f, v...)
}

// Debug logs at debug level.
func Debug(f string, v ...interface{}) { log.Debug(render(f, v)) }

// Info logs at info level.
func Info(f string, v ...interface{}) { log.Info(render(f, v)) }

// Warning logs at warn level (Beego spelled it "Warning").
func Warning(f string, v ...interface{}) { log.Warn(render(f, v)) }

// Error logs at error level.
func Error(f string, v ...interface{}) { log.Error(render(f, v)) }
