package daemon

import (
	"bytes"
	"context"
	"log/slog"
	"path/filepath"
	"testing"
)

func TestOpenCAMakesThenReopensTheSameAuthority(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dir := filepath.Join(t.TempDir(), "tls")
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))
	ca, err := openCA(ctx, log, dir, []string{"core.lan"})
	if err != nil {
		t.Fatal(err)
	}
	again, err := openCA(ctx, log, dir, []string{"core.lan"})
	if err != nil {
		t.Fatal(err)
	}
	if ca.Fingerprint() == "" || ca.Fingerprint() != again.Fingerprint() {
		t.Fatalf("the CA changed between opens: %s, %s", ca.Fingerprint(), again.Fingerprint())
	}
	if _, err := openCA(ctx, log, "/dev/null/tls", nil); err == nil {
		t.Fatal("a CA in a directory that cannot exist")
	}
}
