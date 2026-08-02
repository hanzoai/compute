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
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer"
	cetypes "github.com/aws/aws-sdk-go-v2/service/costexplorer/types"
	"github.com/hanzoai/money"
)

// awsCostReader reads a BYOC AWS account's spend via Cost Explorer
// (ce:GetCostAndUsage). Cost Explorer is account-wide, so the standard access
// key/secret already stored to manage instances suffices — the IAM policy on the
// key must additionally allow ce:GetCostAndUsage (the documented cost-read scope).
type awsCostReader struct {
	client *costexplorer.Client
}

// newAWSCostReader builds the reader. Cost Explorer is a GLOBAL service reachable
// only via us-east-1, so the client is pinned there regardless of the provider's
// machine region. Empty credentials yield ErrCostUnavailable (skip, never guess).
func newAWSCostReader(accessKeyId, accessKeySecret, region string) (CloudCostReader, error) {
	if strings.TrimSpace(accessKeyId) == "" || strings.TrimSpace(accessKeySecret) == "" {
		return nil, ErrCostUnavailable
	}
	cfg, err := config.LoadDefaultConfig(context.TODO(),
		config.WithRegion("us-east-1"),
		config.WithCredentialsProvider(aws.CredentialsProviderFunc(func(ctx context.Context) (aws.Credentials, error) {
			return aws.Credentials{AccessKeyID: accessKeyId, SecretAccessKey: accessKeySecret}, nil
		})),
	)
	if err != nil {
		return nil, fmt.Errorf("aws cost: load config: %w", err)
	}
	return &awsCostReader{client: costexplorer.NewFromConfig(cfg)}, nil
}

// MonthToDateCents queries UnblendedCost from the month's first day through today.
// Cost Explorer's End is EXCLUSIVE, so end = tomorrow includes today's partial spend;
// a MONTHLY granularity over a partial month returns the month-to-date total.
func (r *awsCostReader) MonthToDateCents(ctx context.Context, now time.Time) (int64, error) {
	start := monthStart(now).Format("2006-01-02")
	end := now.UTC().AddDate(0, 0, 1).Format("2006-01-02")

	out, err := r.client.GetCostAndUsage(ctx, &costexplorer.GetCostAndUsageInput{
		TimePeriod:  &cetypes.DateInterval{Start: aws.String(start), End: aws.String(end)},
		Granularity: cetypes.GranularityMonthly,
		Metrics:     []string{"UnblendedCost"},
	})
	if err != nil {
		return 0, fmt.Errorf("aws cost: GetCostAndUsage: %w", err)
	}

	var cents int64
	for _, rbt := range out.ResultsByTime {
		if m, ok := rbt.Total["UnblendedCost"]; ok && m.Amount != nil {
			c, err := money.ParseCents(*m.Amount)
			if err != nil {
				return 0, fmt.Errorf("aws cost: UnblendedCost %q: %w", *m.Amount, err)
			}
			cents += c
		}
	}
	return cents, nil
}
