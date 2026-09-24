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

// transfer.go reads what hosted machines sent out: EC2's NetworkOut metric from
// CloudWatch, through egress like every other call to the hosted account.
//
// The request is CloudWatch's Query API — a form POST answered in XML, the same
// shape as EC2's — built here rather than through the CloudWatch SDK, whose
// current protocol (RPC v2 CBOR) needs a header egress does not carry.
// NetworkOut counts every byte an instance's interfaces send, to the internet
// and to anything else in the region, so it bounds a machine's internet transfer
// from above and is not a measure of it: the sweep stops on it and charges
// nothing for it.

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

// pageDatapoints is the most datapoints one GetMetricData answer carries
// (MaxDatapoints); more come on the next page, by NextToken. At about a hundred
// bytes a datapoint and a few hundred a query, a page stays far inside
// mostAnswer.
const pageDatapoints = 2000

// mostAnswer is the most bytes of one GetMetricData answer read. An answer
// longer than that is refused as an error, never parsed short.
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
		// Complete is every datapoint; PartialData is this page's, with the rest
		// on the next. Anything else — InternalError, Forbidden — is a count
		// CloudWatch could not give, and is not taken as nothing sent.
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
