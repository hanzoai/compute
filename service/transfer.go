// Copyright 2026 Hanzo Industries Inc. All Rights Reserved.
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

// transfer.go reads EC2's NetworkOut from CloudWatch through egress, with
// CloudWatch's Query API (form POST, XML answer): the SDK's RPC v2 CBOR protocol
// needs a header egress does not carry. NetworkOut counts every byte sent,
// in-region included, so the sweep stops on it and charges nothing for it.

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// settle is how long after an hour ends its NetworkOut datapoints are taken as
// complete: basic monitoring publishes five-minute points a few minutes late.
const settle = 15 * time.Minute

// mostQueries is how many instances one GetMetricData asks about.
const mostQueries = 500

// pageDatapoints is MaxDatapoints: a page of it stays far inside mostAnswer.
const pageDatapoints = 2000

// mostAnswer is the most bytes of one answer read; a longer one is an error.
const mostAnswer = 1 << 20

// outbound returns how many bytes each instance sent out in each whole hour of
// [from, to), by instance id and hour mark. An hour with no datapoint sent
// nothing. It is a variable so the sweep can be run with no CloudWatch behind it.
var outbound = readOutbound

// readOutbound asks CloudWatch, through egress, for the hourly Sum of NetworkOut
// of each instance.
func readOutbound(ctx context.Context, instances []string, from, to time.Time) (map[string]map[string]int64, error) {
	region := hostedConfig().Region
	if region == "" {
		return nil, errNoRegion
	}
	hc, err := carried(Credential{Provider: "AWS", Name: hostedLabel, Region: region})
	if err != nil {
		return nil, err
	}
	out := map[string]map[string]int64{}
	for start := 0; start < len(instances); start += mostQueries {
		batch := instances[start:min(len(instances), start+mostQueries)]
		for token := ""; ; {
			page, next, err := metricPage(ctx, hc, region, batch, from, to, token)
			if err != nil {
				return nil, err
			}
			for instance, hours := range page {
				if out[instance] == nil {
					out[instance] = map[string]int64{}
				}
				for hour, bytes := range hours {
					out[instance][hour] += bytes
				}
			}
			if next == "" {
				break
			}
			token = next
		}
	}
	return out, nil
}

// metricAnswer is the part of a GetMetricData answer the meter reads.
type metricAnswer struct {
	Results []struct {
		ID         string    `xml:"Id"`
		Timestamps []string  `xml:"Timestamps>member"`
		Values     []float64 `xml:"Values>member"`
		StatusCode string    `xml:"StatusCode"`
	} `xml:"GetMetricDataResult>MetricDataResults>member"`
	NextToken string `xml:"GetMetricDataResult>NextToken"`
	Code      string `xml:"Error>Code"`
}

// metricPage is one GetMetricData call for batch.
func metricPage(ctx context.Context, hc *http.Client, region string, batch []string, from, to time.Time, token string) (map[string]map[string]int64, string, error) {
	form := url.Values{
		"Action":        {"GetMetricData"},
		"Version":       {"2010-08-01"},
		"StartTime":     {from.UTC().Format(time.RFC3339)},
		"EndTime":       {to.UTC().Format(time.RFC3339)},
		"ScanBy":        {"TimestampAscending"},
		"MaxDatapoints": {strconv.Itoa(pageDatapoints)},
	}
	for i, instance := range batch {
		p := "MetricDataQueries.member." + strconv.Itoa(i+1) + "."
		form.Set(p+"Id", "q"+strconv.Itoa(i))
		form.Set(p+"MetricStat.Metric.Namespace", "AWS/EC2")
		form.Set(p+"MetricStat.Metric.MetricName", "NetworkOut")
		form.Set(p+"MetricStat.Metric.Dimensions.member.1.Name", "InstanceId")
		form.Set(p+"MetricStat.Metric.Dimensions.member.1.Value", instance)
		form.Set(p+"MetricStat.Period", "3600")
		form.Set(p+"MetricStat.Stat", "Sum")
	}
	if token != "" {
		form.Set("NextToken", token)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://monitoring."+region+".amazonaws.com/", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	resp, err := hc.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("cloudwatch: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, mostAnswer+1))
	if err != nil {
		return nil, "", fmt.Errorf("cloudwatch: %w", err)
	}
	if len(body) > mostAnswer {
		return nil, "", fmt.Errorf("cloudwatch answered more than %d bytes for one page", mostAnswer)
	}
	var doc metricAnswer
	if err := xml.Unmarshal(body, &doc); err != nil {
		return nil, "", fmt.Errorf("cloudwatch answered %d with no readable body", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("cloudwatch refused GetMetricData: %d %s", resp.StatusCode, doc.Code)
	}
	out := map[string]map[string]int64{}
	for _, r := range doc.Results {
		i, err := strconv.Atoi(strings.TrimPrefix(r.ID, "q"))
		if err != nil || i < 0 || i >= len(batch) || len(r.Values) != len(r.Timestamps) {
			return nil, "", fmt.Errorf("cloudwatch answered a query it was not asked: %q", r.ID)
		}
		instance := batch[i]
		// PartialData continues on the next page; any other status is an error.
		if r.StatusCode != "Complete" && r.StatusCode != "PartialData" {
			return nil, "", fmt.Errorf("cloudwatch could not count NetworkOut of %s: %q", instance, r.StatusCode)
		}
		if out[instance] == nil {
			out[instance] = map[string]int64{}
		}
		for j, stamp := range r.Timestamps {
			at, err := time.Parse(time.RFC3339, stamp)
			if err != nil {
				return nil, "", fmt.Errorf("cloudwatch answered a timestamp %q", stamp)
			}
			out[instance][hourOf(at)] += int64(math.Round(r.Values[j]))
		}
	}
	return out, doc.NextToken, nil
}
