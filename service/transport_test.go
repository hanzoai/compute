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

package service

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// An outbound call with no deadline waits as long as the socket stays open. In a
// provisioning path that means an org's hold is never released; in the analytics
// path it means the emit goroutine never returns. directHTTP() is the bounded
// client, and this is what stops the next one being written unbounded.
func TestNoUnboundedClient(t *testing.T) {
	bare := regexp.MustCompile(`http\.Client\{\}|http\.DefaultClient`)
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var found int
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(src), "\n") {
			if bare.MatchString(line) {
				found++
				t.Errorf("%s:%d builds an unbounded client — use directHTTP():\n\t%s", f, i+1, strings.TrimSpace(line))
			}
		}
	}
	// The scan must actually be looking at the provider files, or it passes by
	// reading nothing.
	if len(files) < 20 {
		t.Fatalf("scanned only %d files; the glob is wrong", len(files))
	}
	if found == 0 {
		t.Logf("scanned %d files, every outbound client is bounded", len(files))
	}
}
