package daemon

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/davitizhgenti/hostd/core/api"
	"github.com/davitizhgenti/hostd/modules/apps"
	"github.com/davitizhgenti/hostd/modules/audio"
	"github.com/davitizhgenti/hostd/modules/automation"
	"github.com/davitizhgenti/hostd/modules/deploy"
	"github.com/davitizhgenti/hostd/modules/display"
	"github.com/davitizhgenti/hostd/sdk"
)

var updateGolden = flag.Bool("update", false, "rewrite golden files")

// TestOpenAPIGolden keeps docs/openapi.json, the description of the API
// with every built-in module, in step with the manifests. After an
// intended change: go test ./internal/daemon -run OpenAPI -update
func TestOpenAPIGolden(t *testing.T) {
	tmp := t.TempDir()
	mods := []sdk.Module{
		apps.New(apps.Options{NoWatch: true}),
		display.New(display.Options{}),
		audio.New(audio.Options{}),
		deploy.New(deploy.Options{Root: tmp}),
		automation.New(automation.Options{ScenesDir: tmp}),
	}
	var manifests []sdk.Manifest
	for _, m := range mods {
		manifests = append(manifests, m.Manifest())
	}
	got, err := json.MarshalIndent(api.OpenAPI(manifests, "0.1.0-dev"), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	path := filepath.Join("..", "..", "docs", "openapi.json")
	if *updateGolden {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update)", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("docs/openapi.json is out of date: go test ./internal/daemon -run OpenAPI -update")
	}
}
