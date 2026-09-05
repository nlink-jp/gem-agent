package telemetry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"cloud.google.com/go/logging"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// ADR-0068: the gcp backend declares its monitored resource instead of
// letting the library detect one. The declared value is the detection's
// own fallback on a machine that is not Google-hosted, so the records
// are unchanged — only the probe is gone.
func TestGCPResourceIsTheGlobalProjectResource(t *testing.T) {
	r := gcpResource("example-project")
	if r.Type != "global" || r.Labels["project_id"] != "example-project" || len(r.Labels) != 1 {
		t.Fatalf("resource = %v, want type global with the single label project_id", r)
	}
}

// Building the gem-agent logger must not contact the metadata server:
// that contact is the 4.5–7.2 s silent startup the ADR fixes. The
// metadata client honours GCE_METADATA_HOST, so every probe the
// library would make lands on a counting fake instead of the
// link-local address (and the dial timeouts) of a real Mac.
func TestGCPLoggerNeverProbesTheMetadataServer(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	t.Setenv("GCE_METADATA_HOST", strings.TrimPrefix(srv.URL, "http://"))

	// A real client with a lazy, never-used connection: no credentials
	// are looked up and no RPC is made, so nothing here needs a
	// network.
	client, err := logging.NewClient(context.Background(), "example-project",
		option.WithoutAuthentication(),
		option.WithEndpoint("127.0.0.1:1"),
		option.WithGRPCDialOption(grpc.WithTransportCredentials(insecure.NewCredentials())))
	if err != nil {
		t.Fatal(err)
	}
	client.OnError = func(error) {}
	defer func() { _ = client.Close() }()

	newGCPLogger(client, "example-project", "v", "s", "/p")
	if n := hits.Load(); n != 0 {
		t.Fatalf("building the logger contacted the metadata server %d time(s); the resource must be declared, not detected (ADR-0068)", n)
	}

	// Control: the library's default path DOES probe — this is the
	// call the declaration exists to avoid, and the hit proves the
	// fake host is what the library would reach. Detection is memoised
	// process-wide, so this must stay the package's first Logger
	// built without CommonResource.
	// Detection is memoised process-wide: a second run in the same
	// process (-count, a race sweep) cannot observe the probe again, so
	// the control leg runs once per process.
	if !metadataControlRan {
		metadataControlRan = true
		client.Logger("control")
	} else {
		return
	}
	if hits.Load() == 0 {
		t.Fatal("control: the library's resource detection never reached GCE_METADATA_HOST, so this test can no longer observe the probe it guards against — revisit it against the current cloud.google.com/go/logging")
	}
}

// metadataControlRan records that the library's own detection path has
// run in this process (see the control leg above).
var metadataControlRan bool
