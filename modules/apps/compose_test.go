package apps

import (
	"testing"
)

func TestComposeFileBesideAppFile(t *testing.T) {
	f, err := ParseAppFile("/cfg/apps/media.toml", []byte("runner = { type = \"compose\", file = \"media/compose.yaml\" }\n"))
	if err != nil {
		t.Fatal(err)
	}
	if f.Runner.File != "/cfg/apps/media/compose.yaml" {
		t.Fatalf("file %q", f.Runner.File)
	}
}
