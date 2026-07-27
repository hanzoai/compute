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

// Package telemetry is visor's OpenTelemetry seam: ONE place that wires OTel
// traces + metrics to the o11y collector over ZAP (the same OTEL_EXPORTER_OTLP_*
// contract zen-gateway uses to emit GenAI telemetry), and ONE small API the rest
// of visor calls to attribute spans/metrics per org+project.
//
// It is a leaf package — it imports only the OpenTelemetry SDK and beego's logger,
// so service/ and controllers/ can instrument the provision/list/delete/meter
// paths without an import cycle. When O11Y_ENDPOINT is unset the
// pipeline is a no-op (disabled): the global no-op providers back every span and
// counter, so instrumentation stays branch-free and a local/dev run never spams a
// nonexistent collector. Setup is best-effort — a failure logs and disables
// telemetry, never breaks visor.
package telemetry

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"

	"github.com/hanzoai/visor/logs"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	luxtrace "github.com/luxfi/trace"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
)

// defaultServiceName is the OTel service.name for this process unless overridden
// by O11Y_SERVICE_NAME (the standard OTel env knob).
const defaultServiceName = "visor"

// enabled reports whether the real OTLP pipeline was installed. It gates only
// logging/reporting; the instrumentation helpers work regardless (no-op providers
// when disabled), so no caller branches on it.
var enabled bool

// Enabled reports whether visor is exporting real OTel data to an OTLP collector.
func Enabled() bool { return enabled }

// ContextFromRequest extracts any inbound W3C trace context from the request
// headers so a span started for this request continues the caller's (gateway's)
// trace instead of beginning an orphan root. Safe when telemetry is disabled — the
// global no-op propagator returns the request context unchanged.
func ContextFromRequest(r *http.Request) context.Context {
	return otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))
}

// serviceName resolves the OTel service.name (O11Y_SERVICE_NAME or "visor").
func serviceName() string {
	if n := strings.TrimSpace(os.Getenv("O11Y_SERVICE_NAME")); n != "" {
		return n
	}
	return defaultServiceName
}

// o11yEndpoint returns the configured OTLP endpoint (traces-specific or generic),
// the single "is o11y wired" signal. Empty ⇒ telemetry disabled.
func o11yEndpoint() string {
	for _, k := range []string{"O11Y_TRACES_ENDPOINT", "O11Y_ENDPOINT"} {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
}

// Init builds the OTel trace + metric pipelines and installs them as the process
// global providers, returning a shutdown that flushes the last batch. With no OTLP
// endpoint configured it is a no-op and returns a no-op shutdown, so the disabled
// path costs nothing. The HTTP exporters self-configure from the standard
// OTEL_EXPORTER_OTLP_* env vars (endpoint / headers / protocol), matching the
// zen-gateway contract, so the operator wires one endpoint and traces + metrics
// both flow.
func Init(ctx context.Context) func(context.Context) error {
	noop := func(context.Context) error { return nil }

	endpoint := o11yEndpoint()
	if endpoint == "" {
		logs.Info("telemetry: O11Y_ENDPOINT unset — telemetry disabled")
		return noop
	}

	// Propagate W3C trace context (+ baggage) so a span visor starts links to the
	// gateway's incoming trace, giving one end-to-end trace across the edge.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))

	res, err := resource.Merge(resource.Default(), resource.NewSchemaless(
		attribute.String("service.name", serviceName()),
	))
	if err != nil {
		res = resource.Default()
	}

	tracer, err := luxtrace.New(luxtrace.Config{
		ExporterConfig:  luxtrace.ExporterConfig{Type: luxtrace.ZAP, Endpoint: endpoint},
		TraceSampleRate: 1,
		AppName:         serviceName(),
	})
	if err != nil {
		logs.Warning("telemetry: trace exporter init failed: %v — telemetry disabled", err)
		return noop
	}

	// Metrics are scraped, not pushed: luxfi/metric exports Prometheus families
	// over ZAP through its own gatherer, which is a different shape from an OTel
	// metric exporter. Wiring an OTLP one back in to bridge them would drag
	// protobuf and gRPC into this binary for a path nothing reads.
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithResource(res))

	otel.SetMeterProvider(mp)
	enabled = true
	logs.Info("telemetry: traces → %s over ZAP (service=%s)", endpoint, serviceName())

	return func(ctx context.Context) error {
		return errors.Join(tracer.Close(), mp.Shutdown(ctx))
	}
}
