package session

import (
	"os"
	"testing"
)

// The session id is exported for children like the work directory is
// (ADR-0069 addendum 2): an mcp.json args entry `${GEMAGENT_SESSION_ID}`
// expands to it. An empty id exports nothing.
func TestExport(t *testing.T) {
	t.Setenv(EnvVar, "")
	if err := Export(""); err != nil || os.Getenv(EnvVar) != "" {
		t.Fatalf("empty id exported %q (%v)", os.Getenv(EnvVar), err)
	}
	if err := Export("20260905-000540"); err != nil || os.Getenv(EnvVar) != "20260905-000540" {
		t.Fatalf("export: %q (%v)", os.Getenv(EnvVar), err)
	}
}
