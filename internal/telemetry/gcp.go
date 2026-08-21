package telemetry

import (
	"context"
	"fmt"

	"cloud.google.com/go/logging"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdklog "go.opentelemetry.io/otel/sdk/log"
)

// gcpExporter writes audit events straight into Cloud Logging of the
// operator's [gcp] project — the natural default backend (ADR-0035
// §2a): the tool already authenticates there via ADC for Vertex, so
// the audit trail lands next to the model that produced it with zero
// collector infrastructure.
type gcpExporter struct {
	client *logging.Client
	logger *logging.Logger
}

// logName groups gem-agent's entries in the Logs Explorer.
const logName = "gem-agent"

func newGCPExporter(ctx context.Context, project, version, sessionID, projectDir string) (sdklog.Exporter, error) {
	if project == "" {
		return nil, fmt.Errorf("telemetry backend gcp needs [gcp].project")
	}
	client, err := logging.NewClient(ctx, project)
	if err != nil {
		return nil, fmt.Errorf("cloud logging client: %w", err)
	}
	// Export failures route into the same warn-once path as OTLP.
	client.OnError = func(err error) { otel.Handle(err) }
	logger := client.Logger(logName, logging.CommonLabels(map[string]string{
		"service_version": version,
		"session_id":      sessionID,
		"project_dir":     projectDir,
	}))
	return &gcpExporter{client: client, logger: logger}, nil
}

// entryFromRecord maps one OTel log record to a Cloud Logging entry:
// the event name and attributes become a structured JSON payload.
func entryFromRecord(rec sdklog.Record) logging.Entry {
	payload := map[string]any{"event": rec.EventName()}
	rec.WalkAttributes(func(kv attribute.KeyValue) bool {
		payload[string(kv.Key)] = kv.Value.AsInterface()
		return true
	})
	return logging.Entry{
		Timestamp: rec.Timestamp(),
		Severity:  logging.Info,
		Payload:   payload,
	}
}

func (e *gcpExporter) Export(_ context.Context, records []sdklog.Record) error {
	for _, rec := range records {
		e.logger.Log(entryFromRecord(rec)) // async; client batches and flushes
	}
	return nil
}

func (e *gcpExporter) Shutdown(context.Context) error { return e.client.Close() }
func (e *gcpExporter) ForceFlush(context.Context) error {
	return e.logger.Flush()
}
