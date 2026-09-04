package telemetry

import (
	"context"
	"fmt"

	"cloud.google.com/go/logging"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	mrpb "google.golang.org/genproto/googleapis/api/monitoredres"
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
	logger := newGCPLogger(client, project, version, sessionID, projectDir)
	return &gcpExporter{client: client, logger: logger}, nil
}

// gcpResource is the monitored resource every entry carries: the
// `global` resource of the operator's project. It is DECLARED, never
// detected (ADR-0068): left to itself, the library's Logger classifies
// the host by fetching from the GCE metadata server — a plain HTTP GET
// to a link-local address, with the dial timeout retried as transient
// — and on a Mac that fetch intermittently costs 4.5–7.2 s of silent
// startup while the kernel probes for a neighbour that does not exist.
// This value is exactly what the detection falls back to on a machine
// that is not Google-hosted, so declaring it changes no record.
func gcpResource(project string) *mrpb.MonitoredResource {
	return &mrpb.MonitoredResource{
		Type:   "global",
		Labels: map[string]string{"project_id": project},
	}
}

// newGCPLogger builds the entry logger with its resource declared, so
// construction touches no network (pinned by a test that counts
// metadata-server hits).
func newGCPLogger(client *logging.Client, project, version, sessionID, projectDir string) *logging.Logger {
	return client.Logger(logName,
		logging.CommonResource(gcpResource(project)),
		logging.CommonLabels(map[string]string{
			"service_version": version,
			"session_id":      sessionID,
			"project_dir":     projectDir,
		}))
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
