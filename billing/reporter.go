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

package billing

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/hanzoai/visor/logs"
	"github.com/hanzoai/visor/object"
)

type MeterEvent struct {
	MeterID     string            `json:"meterId"`
	UserID      string            `json:"userId"`
	Value       int64             `json:"value"`
	Timestamp   string            `json:"timestamp"`
	Idempotency string            `json:"idempotency"`
	Dimensions  map[string]string `json:"dimensions"`
}

type MeterEventRequest struct {
	Events []MeterEvent `json:"events"`
}

type BillingReporter struct {
	CommerceURL  string
	ServiceToken string
	MeterID      string
	httpClient   *http.Client
}

func NewBillingReporter(commerceURL, serviceToken string) *BillingReporter {
	return &BillingReporter{
		CommerceURL:  commerceURL,
		ServiceToken: serviceToken,
		MeterID:      "doks-node-hours",
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// ReportNodePoolUsage sends a meter event for a single node pool's hourly usage.
func (r *BillingReporter) ReportNodePoolUsage(pool *object.NodePool) error {
	if r.CommerceURL == "" || r.ServiceToken == "" {
		return nil // billing not configured, skip silently
	}

	costPerHour := pool.CostPerHour
	if costPerHour == 0 {
		costPerHour = GetHourlyCostCents(pool.Size)
	}

	now := time.Now().UTC()
	hourTimestamp := now.Format("2006010215") // e.g. "2026022012"

	value := costPerHour * int64(pool.Count)

	userID := pool.OrgID
	if userID == "" {
		userID = pool.Owner
	}

	event := MeterEvent{
		MeterID:     r.MeterID,
		UserID:      userID,
		Value:       value,
		Timestamp:   now.Format(time.RFC3339),
		Idempotency: fmt.Sprintf("doks-%s-%s", pool.PoolID, hourTimestamp),
		Dimensions: map[string]string{
			"size":      pool.Size,
			"cluster":   pool.ClusterID,
			"poolId":    pool.PoolID,
			"poolName":  pool.Name,
			"provider":  pool.Provider,
			"projectId": pool.ProjectID,
		},
	}

	return r.sendMeterEvents([]MeterEvent{event})
}

func (r *BillingReporter) sendMeterEvents(events []MeterEvent) error {
	reqBody := MeterEventRequest{Events: events}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("failed to marshal meter events: %w", err)
	}

	url := fmt.Sprintf("%s/api/v1/billing/meter-events", r.CommerceURL)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return fmt.Errorf("failed to create billing request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+r.ServiceToken)

	resp, err := r.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send billing request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		return fmt.Errorf("billing API returned status %d", resp.StatusCode)
	}

	return nil
}

// ReportAllNodePools fetches all node pools and reports usage for each.
func (r *BillingReporter) ReportAllNodePools() {
	if r.CommerceURL == "" || r.ServiceToken == "" {
		return
	}

	pools := []*object.NodePool{}
	// Query all node pools across all owners
	err := object.GetAllNodePools(&pools)
	if err != nil {
		logs.Warning("billing: failed to get node pools: %v", err)
		return
	}

	for _, pool := range pools {
		if pool.State != "Active" || pool.Count == 0 {
			continue
		}
		err := r.ReportNodePoolUsage(pool)
		if err != nil {
			logs.Warning("billing: failed to report usage for pool %s/%s: %v", pool.Owner, pool.Name, err)
		}
	}
}
