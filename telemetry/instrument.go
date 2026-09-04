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

// instrument.go is the small, typed instrumentation API the rest of compute calls.
// Every span and metric is attributed per org+project through ONE attribute
// builder (orgProjectAttrs), so the whole fleet — provision, list, delete, meter,
// and all three fleet-billing tiers — reports on the same two dimensions the
// architecture threads everywhere. Instruments are created lazily from the global
// providers, so a helper works whether telemetry is enabled (real OTLP export) or
// disabled (global no-op) with no caller-side branching.
package telemetry

import (
	"context"
	"sync"

	"github.com/hanzoai/compute/logs"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// scope is the instrumentation scope name (the OTel "library" that emits the
// telemetry) shared by the tracer and meter.
const scope = "github.com/hanzoai/compute"

var (
	instrumentsOnce sync.Once
	tracer          trace.Tracer
	launchCounter   metric.Int64Counter
	deleteCounter   metric.Int64Counter
	listCounter     metric.Int64Counter
	meteredCounter  metric.Int64Counter // cents metered to commerce, by tier
	skipCounter     metric.Int64Counter // billing sweeps that could not debit, by sweep
)

// ensureInstruments builds the tracer + counters once from the current global
// providers. Called at first use (after Init has installed the providers, or
// against the no-op globals when telemetry is disabled), so instruments are always
// non-nil and the helpers never branch on Enabled().
func ensureInstruments() {
	instrumentsOnce.Do(func() {
		tracer = otel.Tracer(scope)
		m := otel.Meter(scope)
		var err error
		if launchCounter, err = m.Int64Counter("compute.compute.launches",
			metric.WithDescription("compute machines launched, by org/project")); err != nil {
			logs.Warning("telemetry: launch counter: %v", err)
		}
		if deleteCounter, err = m.Int64Counter("compute.compute.deletes",
			metric.WithDescription("compute machines deleted, by org/project")); err != nil {
			logs.Warning("telemetry: delete counter: %v", err)
		}
		if listCounter, err = m.Int64Counter("compute.compute.machines_listed",
			metric.WithDescription("compute machines returned by list, by org/project")); err != nil {
			logs.Warning("telemetry: list counter: %v", err)
		}
		if meteredCounter, err = m.Int64Counter("compute.billing.metered_cents",
			metric.WithDescription("cents metered to commerce, by org/project/tier"),
			metric.WithUnit("{cent}")); err != nil {
			logs.Warning("telemetry: metered counter: %v", err)
		}
		if skipCounter, err = m.Int64Counter("compute.billing.sweeps_skipped",
			metric.WithDescription("billing sweeps that could not debit (metering unconfigured), by sweep")); err != nil {
			logs.Warning("telemetry: skip counter: %v", err)
		}
	})
}

// orgProjectAttrs is the ONE attribute builder: every span and metric carries
// hanzo.org and (when set) hanzo.project, plus any operation-specific extras. The
// project attribute is omitted for the default (empty) project so cardinality and
// the ledger stay clean.
func orgProjectAttrs(org, project string, extra ...attribute.KeyValue) []attribute.KeyValue {
	a := make([]attribute.KeyValue, 0, 2+len(extra))
	a = append(a, attribute.String("hanzo.org", org))
	if project != "" {
		a = append(a, attribute.String("hanzo.project", project))
	}
	return append(a, extra...)
}

// Span starts a span named name, attributed to org+project. When telemetry is
// disabled the global no-op tracer returns a no-op span, so the caller uses the
// returned span unconditionally (always defer span.End()).
func Span(ctx context.Context, name, org, project string, extra ...attribute.KeyValue) (context.Context, trace.Span) {
	ensureInstruments()
	return tracer.Start(ctx, name, trace.WithAttributes(orgProjectAttrs(org, project, extra...)...))
}

// CountLaunch records one compute launch attempt with its result ("launched" or an
// error class) and size, attributed to org+project.
func CountLaunch(ctx context.Context, org, project, size, result string) {
	ensureInstruments()
	if launchCounter == nil {
		return
	}
	launchCounter.Add(ctx, 1, metric.WithAttributes(orgProjectAttrs(org, project,
		attribute.String("size", size), attribute.String("result", result))...))
}

// CountDelete records one compute delete with its result, attributed to org+project.
func CountDelete(ctx context.Context, org, project, result string) {
	ensureInstruments()
	if deleteCounter == nil {
		return
	}
	deleteCounter.Add(ctx, 1, metric.WithAttributes(orgProjectAttrs(org, project,
		attribute.String("result", result))...))
}

// CountList records how many machines a list returned for org+project.
func CountList(ctx context.Context, org, project string, n int) {
	ensureInstruments()
	if listCounter == nil {
		return
	}
	listCounter.Add(ctx, int64(n), metric.WithAttributes(orgProjectAttrs(org, project)...))
}

// CountMetered records cents debited to commerce for org+project under a billing
// tier ("compute", "byoc", "device"). It is the metric mirror of a commerce debit,
// so o11y shows real, per-tier, per-project spend as it is billed.
func CountMetered(ctx context.Context, org, project, tier string, cents int64) {
	ensureInstruments()
	if meteredCounter == nil || cents <= 0 {
		return
	}
	meteredCounter.Add(ctx, cents, metric.WithAttributes(orgProjectAttrs(org, project,
		attribute.String("tier", tier))...))
}

// CountBillingSkip records one billing sweep that could NOT debit because
// metering is unconfigured. It carries no org — the whole point is that no org
// was billed. This is the series "the sweep did not run this hour" alerts on:
// the failure it names is silent everywhere else, because unbilled resources
// keep running perfectly.
func CountBillingSkip(ctx context.Context, sweep string) {
	ensureInstruments()
	if skipCounter == nil {
		return
	}
	skipCounter.Add(ctx, 1, metric.WithAttributes(attribute.String("sweep", sweep)))
}
