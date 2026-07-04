// Copyright 2025 Hanzo Industries Inc. All Rights Reserved.
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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// bigQueryReadScope is the OAuth scope needed to run the billing-export query.
const bigQueryReadScope = "https://www.googleapis.com/auth/bigquery.readonly"

// gcpCostReader reads a BYOC GCP account's spend from its BigQuery BILLING EXPORT
// table — the canonical way GCP exposes spend (Cloud Billing has no "current spend"
// API). GCP therefore needs two things the machine-management path does not: a
// service-account JSON key with BigQuery read access (stored in Provider.
// ClientSecret) and the export table reference "<project>.<dataset>.<table>" (in
// Provider.CostReadScope). Missing either yields ErrCostUnavailable — the honest
// "not wired" skip, never a fabricated figure. The query is issued via the BigQuery
// REST jobs.query endpoint with an authorized client, so no heavy BigQuery client
// dependency is pulled in.
type gcpCostReader struct {
	saJSON  string
	project string
	table   string // fully-qualified "project.dataset.table"
}

// splitGCPExport parses Provider.CostReadScope "project.dataset.table" into the
// billing project (which runs the query job) and the fully-qualified table.
func splitGCPExport(s string) (project, fqTable string) {
	s = strings.TrimSpace(s)
	parts := strings.Split(s, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", ""
	}
	return parts[0], s
}

func newGCPCostReader(clientSecret, costScope string) (CloudCostReader, error) {
	saJSON := strings.TrimSpace(clientSecret)
	project, table := splitGCPExport(costScope)
	if saJSON == "" || project == "" || table == "" {
		return nil, ErrCostUnavailable
	}
	return &gcpCostReader{saJSON: saJSON, project: project, table: table}, nil
}

// bqQueryResponse is the subset of the BigQuery jobs.query response we read: the
// completion flag and the single SUM(cost) cell (BigQuery returns cell values as
// strings under "v").
type bqQueryResponse struct {
	JobComplete bool `json:"jobComplete"`
	Rows        []struct {
		F []struct {
			V any `json:"v"`
		} `json:"f"`
	} `json:"rows"`
}

// MonthToDateCents sums the export table's cost for the current invoice month. The
// export's invoice.month ("YYYYMM") is the canonical month grouping.
func (r *gcpCostReader) MonthToDateCents(ctx context.Context, now time.Time) (int64, error) {
	creds, err := google.CredentialsFromJSON(ctx, []byte(r.saJSON), bigQueryReadScope)
	if err != nil {
		return 0, fmt.Errorf("gcp cost: parse service-account credentials: %w", err)
	}
	client := oauth2.NewClient(ctx, creds.TokenSource)
	client.Timeout = 30 * time.Second

	// month is derived from time (always "YYYYMM"); the table is operator-configured.
	// Neither is caller-controlled, so the interpolation carries no injection surface.
	month := now.UTC().Format("200601")
	query := fmt.Sprintf("SELECT SUM(cost) AS c FROM `%s` WHERE invoice.month = '%s'", r.table, month)

	reqBody, err := json.Marshal(map[string]any{
		"query":        query,
		"useLegacySql": false,
		"timeoutMs":    30000,
	})
	if err != nil {
		return 0, fmt.Errorf("gcp cost: encode query: %w", err)
	}

	url := fmt.Sprintf("https://bigquery.googleapis.com/bigquery/v2/projects/%s/queries", r.project)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(reqBody))
	if err != nil {
		return 0, fmt.Errorf("gcp cost: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(httpReq)
	if err != nil {
		return 0, fmt.Errorf("gcp cost: bigquery query: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return 0, fmt.Errorf("gcp cost: bigquery status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var qr bqQueryResponse
	if err := json.Unmarshal(body, &qr); err != nil {
		return 0, fmt.Errorf("gcp cost: decode response: %w", err)
	}
	if !qr.JobComplete {
		return 0, fmt.Errorf("gcp cost: bigquery job did not complete within timeout (will retry next run)")
	}
	if len(qr.Rows) == 0 || len(qr.Rows[0].F) == 0 {
		return 0, nil // no rows this month → no spend
	}
	return gcpCellToCents(qr.Rows[0].F[0].V), nil
}

// gcpCellToCents converts a BigQuery result cell (a string for numeric columns, or
// null when SUM has no rows) into cents.
func gcpCellToCents(v any) int64 {
	switch x := v.(type) {
	case string:
		return dollarStringToCents(x)
	case float64:
		if x <= 0 {
			return 0
		}
		return int64(math.Round(x * 100))
	default:
		return 0
	}
}
