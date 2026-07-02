// Copyright 2024 Hanzo Industries Inc. All Rights Reserved.
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

package task

import (
	"context"
	"strings"
	"time"

	"github.com/beego/beego/logs"
	"github.com/hanzoai/vm/autoscaler"
	"github.com/hanzoai/vm/billing"
	"github.com/hanzoai/vm/conf"
	"github.com/hanzoai/vm/object"
	"github.com/hanzoai/vm/service"
)

type Ticker struct{}

func NewTicker() *Ticker {
	return &Ticker{}
}

func (t *Ticker) SetupTicker() {
	// delete unused session every hour
	unUsedSessionTicker := time.NewTicker(time.Hour)
	go func() {
		for range unUsedSessionTicker.C {
			t.deleteUnUsedSession()
		}
	}()

	// billing: report node pool usage every hour
	commerceURL := conf.GetConfigString("commerceUrl")
	commerceToken := conf.GetConfigString("commerceToken")
	if commerceURL != "" && commerceToken != "" {
		reporter := billing.NewBillingReporter(commerceURL, commerceToken)
		billingTicker := time.NewTicker(time.Hour)
		go func() {
			for range billingTicker.C {
				reporter.ReportAllNodePools()
			}
		}()
		logs.Info("billing: hourly node pool usage reporting enabled")
	}

	// compute metering: debit every RUNNING /v1 resell machine one hour of its
	// resale price to its owning org, every hour, on the canonical commerce/
	// metering path (same client the launch debit uses). A running bound machine
	// keeps drawing down the org's credit balance. Enablement is decided inside
	// MeterRunningMachines (no-op when commerce or compute is unconfigured), so
	// this is wired unconditionally — no second config gate to drift.
	if service.MeteringConfigured() {
		computeTicker := time.NewTicker(time.Hour)
		go func() {
			for range computeTicker.C {
				service.MeterRunningMachines(context.Background())
			}
		}()
		logs.Info("compute metering: hourly running-machine drawdown enabled")
	}

	// autoscaler: start pod watcher if cluster configs are set
	autoscalerClusters := conf.GetConfigString("autoscalerClusters")
	if autoscalerClusters != "" {
		go t.startAutoscaler(autoscalerClusters)
	}
}

func (t *Ticker) startAutoscaler(clustersConfig string) {
	// Format: "clusterID:providerName:owner,clusterID:providerName:owner"
	var clusters []autoscaler.ClusterConfig
	for _, entry := range strings.Split(clustersConfig, ",") {
		parts := strings.SplitN(strings.TrimSpace(entry), ":", 3)
		if len(parts) != 3 {
			logs.Warning("autoscaler: invalid cluster config entry: %s (expected clusterID:providerName:owner)", entry)
			continue
		}
		clusters = append(clusters, autoscaler.ClusterConfig{
			ClusterID:    parts[0],
			ProviderName: parts[1],
			Owner:        parts[2],
		})
	}

	if len(clusters) == 0 {
		logs.Warning("autoscaler: no valid cluster configs found")
		return
	}

	watcher, err := autoscaler.NewPodWatcher(clusters)
	if err != nil {
		logs.Warning("autoscaler: failed to initialize pod watcher: %v", err)
		return
	}

	watcher.Start(context.Background())
}

func (t *Ticker) deleteUnUsedSession() {
	sessions, err := object.GetSessionsByStatus([]string{object.NoConnect, object.Connecting})
	if err != nil {
		logs.Info("failed to get unused sessions: ", err)
		return
	}

	now := time.Now()
	for _, session := range sessions {
		if session.StartTime != "" {
			startTime, err := time.ParseInLocation(time.RFC3339, session.StartTime, time.Local)
			if err != nil {
				continue
			}
			if now.Sub(startTime).Hours() > 1 {
				_, err := object.DeleteSessionById(session.GetId())
				if err != nil {
					logs.Info("delete session failed: ", err)
					return
				}
			}
		} else {
			_, err := object.DeleteSessionById(session.GetId())
			if err != nil {
				logs.Info("delete session failed: ", err)
				return
			}
		}
	}
}
