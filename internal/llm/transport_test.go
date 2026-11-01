package llm

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"google.golang.org/genai"
)

// The shipped values are the measured ones (ADR-0100 §1); changing
// them is a new decision, not a tidy-up.
func TestVertexTransportShipsTheHealthCheck(t *testing.T) {
	if healthPingAfter != 31*time.Second || healthPingTimeout != 15*time.Second {
		t.Fatalf("health check = %v/%v, want the measured 31s/15s", healthPingAfter, healthPingTimeout)
	}
	tr := vertexTransport(healthPingAfter, healthPingTimeout)
	if tr.HTTP2 == nil || tr.HTTP2.SendPingTimeout != healthPingAfter || tr.HTTP2.PingTimeout != healthPingTimeout {
		t.Fatalf("HTTP2 config = %+v, want the health check", tr.HTTP2)
	}
	if !tr.ForceAttemptHTTP2 {
		t.Error("ForceAttemptHTTP2 lost: without h2 there is no ping")
	}
	if tr.MaxIdleConnsPerHost != 100 {
		t.Errorf("MaxIdleConnsPerHost = %d, want the SDK base's 100", tr.MaxIdleConnsPerHost)
	}
	if tr == http.DefaultTransport {
		t.Error("the default transport itself was modified")
	}
}

// freezer relays connections to a local server and can freeze them:
// from then on every byte in either direction is dropped, both sockets
// stay open and no write blocks — a path whose packets vanish.
type freezer struct {
	target string
	once   sync.Once
	frozen chan struct{}
}

func (f *freezer) freeze() { f.once.Do(func() { close(f.frozen) }) }

func (f *freezer) dial(ctx context.Context, network, _ string) (net.Conn, error) {
	var d net.Dialer
	up, err := d.DialContext(ctx, network, f.target)
	if err != nil {
		return nil, err
	}
	client, server := net.Pipe()
	pump := func(dst io.Writer, src io.Reader) {
		buf := make([]byte, 32<<10)
		for {
			n, err := src.Read(buf)
			if n > 0 {
				select {
				case <-f.frozen:
				default:
					if _, werr := dst.Write(buf[:n]); werr != nil {
						return
					}
				}
			}
			if err != nil {
				return
			}
		}
	}
	// Either side ending ends both: the server's handler must see its
	// connection close when the client gives up, or it never returns.
	var closeOnce sync.Once
	closeBoth := func() { closeOnce.Do(func() { _ = up.Close(); _ = server.Close() }) }
	go func() { pump(up, server); closeBoth() }()
	go func() { pump(server, up); closeBoth() }()
	return client, nil
}

// TestHealthCheckNamesAFrozenConnection drives the shipped transport
// and ChatStream through genai against a local TLS HTTP/2 server, and
// freezes the connection once the request has arrived — before the
// response headers, and after the first chunk. Those are the two paths
// the transport's error takes (doRequest, and the stream scanner);
// both must come back as ErrConnectionLost, quickly. The control runs
// the same freeze with no health check: then nothing names the dead
// connection, which is what makes the ping the thing being tested.
func TestHealthCheckNamesAFrozenConnection(t *testing.T) {
	for _, tc := range []struct {
		name        string
		firstChunk  bool
		healthCheck bool
	}{
		{"before the response headers", false, true},
		{"after the first chunk", true, true},
		{"control: no health check", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			arrived := make(chan struct{}, 1)
			done := make(chan struct{})
			srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.ProtoMajor != 2 {
					t.Errorf("request arrived over %s — no h2, no ping", r.Proto)
				}
				if tc.firstChunk {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"hel"}]}}]}`+"\n\n")
					w.(http.Flusher).Flush()
				}
				arrived <- struct{}{}
				select {
				case <-r.Context().Done():
				case <-done:
				}
			}))
			srv.EnableHTTP2 = true
			srv.StartTLS()
			defer srv.Close()
			defer close(done) // runs first: the handler returns before Close waits on it

			f := &freezer{target: srv.Listener.Addr().String(), frozen: make(chan struct{})}
			pingAfter := 300 * time.Millisecond
			if !tc.healthCheck {
				pingAfter = 0 // no health check
			}
			tr := vertexTransport(pingAfter, 300*time.Millisecond)
			tr.TLSClientConfig = srv.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
			tr.DialContext = f.dial
			defer tr.CloseIdleConnections()

			wait := 5 * time.Second
			if !tc.healthCheck {
				wait = 2 * time.Second // the control waits out its deadline
			}
			ctx, cancel := context.WithTimeout(context.Background(), wait)
			defer cancel()
			client, err := genai.NewClient(ctx, &genai.ClientConfig{
				Project:     "p",
				Location:    "global",
				Backend:     genai.BackendVertexAI,
				HTTPOptions: genai.HTTPOptions{BaseURL: srv.URL + "/"},
				HTTPClient:  &http.Client{Transport: tr},
			})
			if err != nil {
				t.Fatal(err)
			}
			v := newVertex(client, "m", "off", "", false)
			var chunks int
			// After-the-chunk freezes on the client's first chunk, not on
			// the server's flush: bytes still in the relay would be
			// dropped and the case would silently become the other one.
			v.SetObserver(func(ev StreamEvent) {
				if ev.Kind == "chunk" {
					chunks++
					f.freeze()
				}
			})
			if !tc.firstChunk {
				go func() {
					select {
					case <-arrived:
						f.freeze()
					case <-ctx.Done():
					}
				}()
			}

			start := time.Now()
			_, err = v.ChatStream(ctx, "", []Message{{Role: RoleUser, Content: "hi"}}, nil, nil)
			took := time.Since(start)
			if tc.firstChunk && chunks == 0 {
				t.Fatalf("no chunk reached the observer before the freeze (err=%v) — the case did not run", err)
			}
			if !tc.healthCheck {
				if errors.Is(err, ErrConnectionLost) || ctx.Err() == nil {
					t.Fatalf("without a health check the frozen stream ended on its own after %v: %v", took, err)
				}
				return
			}
			if !errors.Is(err, ErrConnectionLost) {
				t.Fatalf("err = %v, want ErrConnectionLost", err)
			}
			select {
			case <-f.frozen:
			default:
				t.Fatalf("named a live connection before the freeze (after %v): the ping timed out on its own", took)
			}
			if took > 3*time.Second {
				t.Errorf("named after %v; the health check allows about 600ms", took)
			}
		})
	}
}

func TestConnectionLostMatchesOnlyTheTransportsText(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{errors.New(`doRequest: error sending request: Post "x": http2: client connection lost`), true},
		{errors.New("http2: client connection lost"), true},
		{errors.New("http2: client connection force closed via ClientConn.Close"), false},
		{context.DeadlineExceeded, false},
		{nil, false},
	} {
		if got := connectionLost(tc.err); got != tc.want {
			t.Errorf("connectionLost(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
}
