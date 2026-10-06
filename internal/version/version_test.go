package version

import "testing"

func TestIsDev(t *testing.T) {
	for v, want := range map[string]bool{
		"dev":         true,
		"v0.1.0-dev":  true,
		"v0.1.0":      false,
		"v0.2.0-rc.1": false,
	} {
		Version = v
		if got := IsDev(); got != want {
			t.Errorf("IsDev() with %q = %v, want %v", v, got, want)
		}
	}
}
