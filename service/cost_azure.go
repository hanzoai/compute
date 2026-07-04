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
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/costmanagement/armcostmanagement"
)

// azureCostReader reads a BYOC Azure subscription's spend via the Cost Management
// Query API (a month-to-date ActualCost sum over the subscription scope). Beyond the
// (clientId, clientSecret) client credentials used to manage VMs, Azure needs the
// AUTH TENANT and the SUBSCRIPTION to scope the query — both carried in
// Provider.CostReadScope as "<tenantId>/<subscriptionId>". Missing either yields
// ErrCostUnavailable (skip, never guess). The credential's service principal must
// have the Cost Management Reader role on the subscription (documented cost-read
// scope).
type azureCostReader struct {
	client *armcostmanagement.QueryClient
	scope  string // "/subscriptions/{subscriptionId}"
}

// splitAzureScope parses Provider.CostReadScope "<tenantId>/<subscriptionId>".
func splitAzureScope(s string) (tenant, sub string) {
	parts := strings.SplitN(strings.TrimSpace(s), "/", 2)
	if len(parts) != 2 {
		return "", ""
	}
	return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
}

func newAzureCostReader(clientId, clientSecret, costScope string) (CloudCostReader, error) {
	tenantID, subID := splitAzureScope(costScope)
	if strings.TrimSpace(clientId) == "" || strings.TrimSpace(clientSecret) == "" || tenantID == "" || subID == "" {
		return nil, ErrCostUnavailable
	}
	cred, err := azidentity.NewClientSecretCredential(tenantID, clientId, clientSecret, nil)
	if err != nil {
		return nil, fmt.Errorf("azure cost: credential: %w", err)
	}
	client, err := armcostmanagement.NewQueryClient(cred, nil)
	if err != nil {
		return nil, fmt.Errorf("azure cost: query client: %w", err)
	}
	return &azureCostReader{client: client, scope: "/subscriptions/" + subID}, nil
}

// MonthToDateCents runs a MonthToDate ActualCost query summing Cost over the
// subscription, then reads the cost column out of the returned rows. Azure has no
// historical-month selector on MonthToDate, so `now` is unused.
func (r *azureCostReader) MonthToDateCents(ctx context.Context, _ time.Time) (int64, error) {
	resp, err := r.client.Usage(ctx, r.scope, armcostmanagement.QueryDefinition{
		Type:      to.Ptr(armcostmanagement.ExportTypeActualCost),
		Timeframe: to.Ptr(armcostmanagement.TimeframeTypeMonthToDate),
		Dataset: &armcostmanagement.QueryDataset{
			Aggregation: map[string]*armcostmanagement.QueryAggregation{
				"totalCost": {Name: to.Ptr("Cost"), Function: to.Ptr(armcostmanagement.FunctionTypeSum)},
			},
		},
	}, nil)
	if err != nil {
		return 0, fmt.Errorf("azure cost: usage query: %w", err)
	}
	return azureRowsCostCents(resp.Properties), nil
}

// azureRowsCostCents finds the cost column by name and sums it across the result
// rows (a MonthToDate total is normally a single row: [cost, currency]). Rows are
// decoded as `any`, so cost is a JSON number; it is summed as dollars and converted
// to cents.
func azureRowsCostCents(props *armcostmanagement.QueryProperties) int64 {
	if props == nil {
		return 0
	}
	costIdx := -1
	for i, c := range props.Columns {
		if c != nil && c.Name != nil && strings.Contains(strings.ToLower(*c.Name), "cost") {
			costIdx = i
			break
		}
	}
	if costIdx < 0 {
		return 0
	}
	var dollars float64
	for _, row := range props.Rows {
		if costIdx < len(row) {
			if f, ok := anyToFloat(row[costIdx]); ok {
				dollars += f
			}
		}
	}
	if dollars <= 0 {
		return 0
	}
	return int64(math.Round(dollars * 100))
}

// anyToFloat coerces a JSON-decoded numeric cell to float64.
func anyToFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case float32:
		return float64(x), true
	case int64:
		return float64(x), true
	case int:
		return float64(x), true
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	default:
		return 0, false
	}
}
