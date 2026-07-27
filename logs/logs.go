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

// Package logs writes visor's leveled log lines.
//
// It exists because the beego rip rewrote every beego logs import to point
// here without anything being put here. Twenty-two call sites across
// autoscaler, billing, telemetry, task and controllers compiled against a
// package that was never written, so visor did not build at all — at any pin,
// for as long as the rewrite has been in the tree.
//
// One shape: a format string and its arguments. beego's logs took an
// interface{} first argument and reached for Sprintf only when arguments
// followed, so logs.Info("failed: ", err) produced a line ending in
// %!(EXTRA *errors.errorString=…) rather than the error. Requiring a format
// makes that shape a compile-time fact instead of a runtime surprise.
package logs

import (
	"fmt"
	"log/slog"
)

func Info(format string, v ...any)    { slog.Info(line(format, v)) }
func Warning(format string, v ...any) { slog.Warn(line(format, v)) }
func Error(format string, v ...any)   { slog.Error(line(format, v)) }

// line formats only when there is something to format. Sprintf on a bare
// string turns a literal percent — a rate, a percentage in a message — into
// %!d(MISSING), so a message with no arguments is passed through as written.
func line(format string, v []any) string {
	if len(v) == 0 {
		return format
	}
	return fmt.Sprintf(format, v...)
}
