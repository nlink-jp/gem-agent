// Package telemetry emits gem-agent's audit events as OpenTelemetry
// log records over OTLP (ADR-0035). Metadata only — prompts, model
// responses, file contents, and thoughts never travel this channel;
// the local transcript stays the full record. Disabled is the zero
// value: every method on a nil or no-op Sink is free, so call sites
// never branch.
package telemetry

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	otellog "go.opentelemetry.io/otel/log"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// Config mirrors the [telemetry] table. It lives ONLY in the
// operator's global config — a project file cannot enable telemetry or
// redirect the endpoint (ADR-0035 §2): the exporter is an egress
// channel.
type Config struct {
	Enabled bool `toml:"enabled"`
	// Backend: "gcp" (default — Cloud Logging in the [gcp] project,
	// riding the same ADC as Vertex), "otlp-grpc", or "otlp-http".
	Backend  string `toml:"backend"`
	Endpoint string `toml:"endpoint"` // otlp-* only
	Insecure bool   `toml:"insecure"` // otlp-* only
	// HeadersFile names a JSON file of OTLP auth headers
	// ({"Authorization": "Bearer …"}). A file survives launchd, cron,
	// and shells that never sourced the profile — the environment
	// variable does not (operator observation). Must be mode 0600;
	// unset falls back to the standard OTEL_EXPORTER_OTLP_HEADERS.
	HeadersFile string `toml:"headers_file"`
}

// Sink emits audit events. The zero/nil Sink is a no-op.
type Sink struct {
	// mu guards logger and provider on the root sink: Restart swaps
	// them when /clear starts a new session (ADR-0071 addendum), and
	// every holder of the pointer — the agent, the riskbook runner,
	// the deferred closers — keeps working through the same Sink.
	mu        sync.RWMutex
	logger    otellog.Logger
	provider  *sdklog.LoggerProvider
	sessionID string // the id the resource carries; Restart replaces it
	// label, when set, marks every event with agent=<label>: records
	// emitted on behalf of a delegated child loop (ADR-0037). Empty for
	// the main loop, so existing queries keep matching unchanged.
	label string
	// parent is set on a Sub sink: it reads the root's current logger
	// and provider, so a child created before a Restart follows it.
	parent *Sink
	// build rebuilds the exporter and provider for a session id — what
	// New captured — so Restart can re-resource the sink in place. nil
	// on sinks that cannot be restarted (Restart is then a no-op).
	build func(ctx context.Context, sessionID string) (otellog.Logger, *sdklog.LoggerProvider, error)
}

// current returns the logger and provider in force, following a Sub
// sink to its root.
func (s *Sink) current() (otellog.Logger, *sdklog.LoggerProvider) {
	root := s
	if s.parent != nil {
		root = s.parent
	}
	root.mu.RLock()
	defer root.mu.RUnlock()
	return root.logger, root.provider
}

// Restart re-resources the sink for a new session id (ADR-0071
// addendum: /clear is a new session, so its events must carry the new
// id like everything else the session exports). The old provider is
// flushed and shut down; holders of the pointer, Sub sinks included,
// continue on the new one. Nil-safe; a sink without a builder keeps
// its resource.
func (s *Sink) Restart(ctx context.Context, sessionID string) error {
	if s == nil || s.build == nil {
		return nil
	}
	logger, provider, err := s.build(ctx, sessionID)
	if err != nil {
		return err
	}
	s.mu.Lock()
	old := s.provider
	s.logger, s.provider, s.sessionID = logger, provider, sessionID
	s.mu.Unlock()
	if old != nil {
		sctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = old.Shutdown(sctx)
	}
	return nil
}

// Nop returns the disabled sink.
func Nop() *Sink { return nil }

// Sub returns a sink whose every event carries agent=<label> — the
// audit attribution for a delegated agent loop (ADR-0037). It shares
// the parent's provider; call Shutdown only on the parent. Nil-safe
// like every Sink method.
func (s *Sink) Sub(label string) *Sink {
	if s == nil {
		return s
	}
	if _, provider := s.current(); provider == nil {
		return s
	}
	root := s
	if s.parent != nil {
		root = s.parent
	}
	return &Sink{parent: root, label: label}
}

// New builds an OTLP-backed sink. Telemetry must never hurt the
// session (ADR-0035 §4): export failures reach stderr once via the
// global error handler and degrade silently after.
func New(ctx context.Context, cfg Config, gcpProject, version, sessionID, projectDir string) (*Sink, error) {
	build := func(ctx context.Context, sessionID string) (otellog.Logger, *sdklog.LoggerProvider, error) {
		return newProvider(ctx, cfg, gcpProject, version, sessionID, projectDir)
	}
	logger, provider, err := build(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	return &Sink{logger: logger, provider: provider, sessionID: sessionID, build: build}, nil
}

// SessionID is the session the sink's resource currently names — what
// a call captures at its start, so a late return after a /clear can
// say which session it belongs to. Empty for a nil sink.
func (s *Sink) SessionID() string {
	if s == nil {
		return ""
	}
	root := s
	if s.parent != nil {
		root = s.parent
	}
	root.mu.RLock()
	defer root.mu.RUnlock()
	return root.sessionID
}

// newProvider builds the exporter and the resourced provider for one
// session id — once at startup, and again on every /clear.
func newProvider(ctx context.Context, cfg Config, gcpProject, version, sessionID, projectDir string) (otellog.Logger, *sdklog.LoggerProvider, error) {
	var exp sdklog.Exporter
	var err error
	switch cfg.Backend {
	case "", "gcp":
		exp, err = newGCPExporter(ctx, gcpProject, version, sessionID, projectDir)
	case "otlp-grpc":
		headers, herr := loadHeaders(cfg.HeadersFile)
		if herr != nil {
			return nil, nil, herr
		}
		opts := []otlploggrpc.Option{otlploggrpc.WithEndpoint(cfg.Endpoint)}
		if cfg.Insecure {
			opts = append(opts, otlploggrpc.WithInsecure())
		}
		if headers != nil {
			opts = append(opts, otlploggrpc.WithHeaders(headers))
		}
		exp, err = otlploggrpc.New(ctx, opts...)
	case "otlp-http":
		headers, herr := loadHeaders(cfg.HeadersFile)
		if herr != nil {
			return nil, nil, herr
		}
		opts := []otlploghttp.Option{otlploghttp.WithEndpoint(cfg.Endpoint)}
		if cfg.Insecure {
			opts = append(opts, otlploghttp.WithInsecure())
		}
		if headers != nil {
			opts = append(opts, otlploghttp.WithHeaders(headers))
		}
		exp, err = otlploghttp.New(ctx, opts...)
	default:
		return nil, nil, fmt.Errorf("[telemetry].backend must be gcp, otlp-grpc, or otlp-http (got %q)", cfg.Backend)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("telemetry exporter: %w", err)
	}

	res, err := resource.New(ctx,
		resource.WithHost(),
		resource.WithAttributes(
			semconv.ServiceName("gem-agent"),
			semconv.ServiceVersion(version),
			attribute.String("session.id", sessionID),
			attribute.String("project.dir", projectDir),
		))
	if err != nil {
		res = resource.Default()
	}
	provider := sdklog.NewLoggerProvider(
		sdklog.WithProcessor(sdklog.NewBatchProcessor(exp)),
		sdklog.WithResource(res),
	)
	otel.SetErrorHandler(warnOnceHandler())
	return provider.Logger("gem-agent"), provider, nil
}

// warnOnceHandler prints the first export failure and swallows the
// rest — an unreachable collector must not spam a working session.
func warnOnceHandler() otel.ErrorHandlerFunc {
	var once sync.Once
	return func(err error) {
		once.Do(func() {
			fmt.Fprintf(os.Stderr, "warning: telemetry export failing (reported once): %v\n", err)
		})
	}
}

// Shutdown flushes with a hard cap — exit must not hang on a dead
// collector.
func (s *Sink) Shutdown() {
	if s == nil {
		return
	}
	_, provider := s.current()
	if provider == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = provider.Shutdown(ctx)
}

// clip bounds attribute values: audit metadata, not payload transport.
func clip(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

func (s *Sink) emit(name string, attrs ...attribute.KeyValue) {
	if s == nil {
		return
	}
	logger, provider := s.current()
	if provider == nil {
		return
	}
	var rec otellog.Record
	rec.SetTimestamp(time.Now())
	rec.SetSeverity(otellog.SeverityInfo)
	rec.SetEventName(name)
	rec.SetBody(attribute.StringValue(name))
	if s.label != "" {
		rec.AddAttributes(attribute.String("agent", s.label))
	}
	rec.AddAttributes(attrs...)
	logger.Emit(context.Background(), rec)
}

// --- audit events (ADR-0035 §1) ---

func (s *Sink) SessionStart(model string, sandbox, autoApprove bool, mcpServers int) {
	s.emit("session.start",
		attribute.String("model", model),
		attribute.Bool("sandbox", sandbox),
		attribute.Bool("auto_approve", autoApprove),
		attribute.Int("mcp_servers", mcpServers))
}

func (s *Sink) SessionEnd() { s.emit("session.end") }

func (s *Sink) TurnEnd(rounds int, dur time.Duration, errClass string) {
	s.emit("turn.end",
		attribute.Int("rounds", rounds),
		attribute.Int64("duration_ms", dur.Milliseconds()),
		attribute.String("outcome", errClass))
}

// ToolCall is the audit core: what ran, for how long, with what
// outcome. detail is the clipped argument summary — an audit trail of
// "shell_exec ran" without the command is not an audit trail
// (ADR-0035 §3).
// purpose is the model's own declaration of why it wanted the call
// (ADR-0047), recorded so the log can answer "why did it try that?"
// long after the thought stream that carried the motivation is gone.
// Empty when the model omitted it — the absence is part of the record.
func (s *Sink) ToolCall(name string, mutating bool, detail, purpose string, dur time.Duration, outcome string) {
	s.emit("tool.call",
		attribute.String("tool", name),
		attribute.Bool("mutating", mutating),
		attribute.String("detail", clip(detail, 300)),
		attribute.String("purpose", clip(purpose, 300)),
		attribute.Int64("duration_ms", dur.Milliseconds()),
		attribute.String("outcome", outcome))
}

// ToolLateReturn closes the audit gap behind an abandoned call
// (ADR-0065 §2): the floor reported outcome=abandoned at the grace;
// this says the call did return, when, and how. Best-effort — a
// return after Shutdown is lost, like any event after Shutdown.
//
// originSession is the session the call started in. After a /clear the
// sink's resource names the new session, so the event carries the
// origin as an attribute — without it the audit trail attributed the
// old session's effect to the new one (review after v0.68.1).
func (s *Sink) ToolLateReturn(name string, mutating bool, dur time.Duration, outcome, originSession string) {
	s.emit("tool.late_return",
		attribute.String("tool", name),
		attribute.Bool("mutating", mutating),
		attribute.Int64("duration_ms", dur.Milliseconds()),
		attribute.String("outcome", outcome),
		attribute.String("origin_session_id", originSession))
}

// Approval records who or what let a call through (or refused it):
// source is operator / allowlist / policy_never / auto_rule /
// auto_model.
func (s *Sink) Approval(tool, decision, source string, mustPrompt bool, reason string) {
	s.emit("approval.decision",
		attribute.String("tool", tool),
		attribute.String("decision", decision),
		attribute.String("source", source),
		attribute.Bool("must_prompt", mustPrompt),
		attribute.String("reason", clip(reason, 200)))
}

// Usage carries the same buckets as the transcript's accounting record
// (ADR-0057, ADR-0066): thoughts bill as output, cached is a discounted
// share of the prompt, and tool_prompt is built-in tool results fed
// back as input — so a fleet-wide figure computed from this stream
// uses the same arithmetic as one computed from a local transcript
// (prompt + output + thoughts + tool_prompt == total). Counts only —
// ADR-0035's metadata-only rule is untouched.
func (s *Sink) Usage(promptTok, outputTok, thoughtTok, cachedTok, toolPromptTok, totalTok int) {
	s.emit("model.usage",
		attribute.Int("prompt_tokens", promptTok),
		attribute.Int("output_tokens", outputTok),
		attribute.Int("thought_tokens", thoughtTok),
		attribute.Int("cached_tokens", cachedTok),
		attribute.Int("tool_prompt_tokens", toolPromptTok),
		attribute.Int("total_tokens", totalTok))
}

// Reload records an in-session integration reload (ADR-0039): a
// session whose tool surface changed mid-way must not look, in the
// audit log, like the session that started.
func (s *Sink) Reload(kind string, servers, tools int) {
	s.emit("integration.reload",
		attribute.String("kind", kind),
		attribute.Int("servers", servers),
		attribute.Int("tools", tools))
}

func (s *Sink) Compaction(replaced, kept int) {
	s.emit("compaction",
		attribute.Int("messages_summarised", replaced),
		attribute.Int("messages_kept", kept))
}

func (s *Sink) MediaUpload(bytes int64, uri string) {
	s.emit("media.upload",
		attribute.Int64("bytes", bytes),
		attribute.String("uri", clip(uri, 200)))
}

// --- test support ---

// RecordedEvent is one captured audit event (test support).
type RecordedEvent struct {
	Name  string
	Attrs map[string]string
}

// Recording captures events in memory so other packages can assert on
// the audit stream without a collector.
type Recording struct {
	mu     sync.Mutex
	events []RecordedEvent
}

// Events returns a copy of everything captured so far.
func (r *Recording) Events() []RecordedEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]RecordedEvent(nil), r.events...)
}

func (r *Recording) Export(_ context.Context, records []sdklog.Record) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, rec := range records {
		ev := RecordedEvent{Name: rec.EventName(), Attrs: map[string]string{}}
		rec.WalkAttributes(func(kv attribute.KeyValue) bool {
			ev.Attrs[string(kv.Key)] = kv.Value.String()
			return true
		})
		r.events = append(r.events, ev)
	}
	return nil
}
func (r *Recording) Shutdown(context.Context) error   { return nil }
func (r *Recording) ForceFlush(context.Context) error { return nil }

// NewRecording returns a Sink whose events land in the returned
// Recording, synchronously.
func NewRecording() (*Sink, *Recording) {
	rec := &Recording{}
	build := func(context.Context, string) (otellog.Logger, *sdklog.LoggerProvider, error) {
		provider := sdklog.NewLoggerProvider(sdklog.WithProcessor(sdklog.NewSimpleProcessor(rec)))
		return provider.Logger("recording"), provider, nil
	}
	logger, provider, _ := build(context.Background(), "")
	return &Sink{logger: logger, provider: provider, sessionID: "recording", build: build}, rec
}

// loadHeaders reads the OTLP auth-header file: a flat JSON object of
// header name → value. nil with no error when no file is configured
// (the SDK then honors OTEL_EXPORTER_OTLP_HEADERS as usual). The file
// holds secrets, so anything but owner-only permissions is refused —
// the same discipline as an SSH key.
func loadHeaders(path string) (map[string]string, error) {
	if path == "" {
		return nil, nil
	}
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("headers_file: %w", err)
		}
		path = home + path[1:]
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("headers_file: %w", err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("headers_file %s is mode %04o — it holds credentials; chmod 600 it", path, info.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("headers_file: %w", err)
	}
	var headers map[string]string
	if err := json.Unmarshal(data, &headers); err != nil {
		return nil, fmt.Errorf("headers_file %s: %v (expected a flat JSON object of header name → value)", path, err)
	}
	if len(headers) == 0 {
		return nil, fmt.Errorf("headers_file %s is empty", path)
	}
	return headers, nil
}
