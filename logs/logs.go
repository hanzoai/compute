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
// It is log/slog underneath, and the whole package is the signature: a format
// string and its arguments, always in that order.
//
// That signature is the point. The framework logger this replaced took an
// interface{} first argument and reached for Sprintf only when arguments
// followed, so logs.Info("failed: ", err) produced a line ending in
// %!(EXTRA *errors.errorString=…) rather than the error — a message that lost
// the one fact it was written to carry, and lost it at runtime, in the log,
// where nobody was going to be looking. Requiring a format makes that shape a
// compile-time fact instead.
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
