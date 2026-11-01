package llm

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"cloud.google.com/go/auth/credentials"
	"cloud.google.com/go/auth/httptransport"
)

// The connection's health check (ADR-0100): an HTTP/2 connection that
// receives no frame for healthPingAfter is pinged, and one that leaves
// the ping unanswered for healthPingTimeout is closed — the stream in
// flight fails with ErrConnectionLost. These are the values the SDK's
// own client carried (cloud.google.com/go/auth sets ReadIdleTimeout
// 31s; PingTimeout defaults to 15s), measured against the live
// endpoint in pinghealth_live_test.go: a live connection answered
// every ping through 205s of silence, a frozen one failed 44-46s
// after the freeze. They are owned here so a dependency update cannot
// drop them unnoticed — the TUI no longer warns about silence because
// of them.
const (
	healthPingAfter   = 31 * time.Second
	healthPingTimeout = 15 * time.Second
)

// ErrConnectionLost marks a stream whose connection the transport's
// health check gave up on. The agent says it to the operator in their
// words; the wrapped error keeps the transport's own text.
var ErrConnectionLost = errors.New("the connection to Vertex AI was lost")

// connectionLost reports whether err is the transport closing a
// connection that stopped answering. Matched on text: the HTTP/2
// transport exports no sentinel for it, and the offline test pins the
// text on both paths it takes (before and after the response headers).
func connectionLost(err error) bool {
	return err != nil && strings.Contains(err.Error(), "http2: client connection lost")
}

// vertexTransport is the base transport for the Vertex client: the
// default transport's settings, the idle-connection cap the SDK's base
// sets, and the health check. ForceAttemptHTTP2 survives the clone, so
// ALPN still negotiates h2 — without h2 there is no ping at all.
func vertexTransport(pingAfter, pingTimeout time.Duration) *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.MaxIdleConnsPerHost = 100
	t.HTTP2 = &http.HTTP2Config{SendPingTimeout: pingAfter, PingTimeout: pingTimeout}
	return t
}

// vertexHTTPClient puts Google's auth on base the way genai.NewClient
// does when it is given no client: Application Default Credentials
// with the cloud-platform scope, and the credentials' quota project as
// X-Goog-User-Project. Handed a client, genai adds no auth of its own.
func vertexHTTPClient(ctx context.Context, base http.RoundTripper) (*http.Client, error) {
	creds, err := credentials.DetectDefault(&credentials.DetectOptions{
		Scopes: []string{"https://www.googleapis.com/auth/cloud-platform"},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to find default credentials: %w", err)
	}
	quota, err := creds.QuotaProjectID(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get quota project ID: %w", err)
	}
	return httptransport.NewClient(&httptransport.Options{
		Credentials:      creds,
		Headers:          http.Header{"X-Goog-User-Project": []string{quota}},
		BaseRoundTripper: base,
	})
}
