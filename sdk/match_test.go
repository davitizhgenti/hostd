package sdk

import "testing"

func TestMatchType(t *testing.T) {
	for _, tc := range []struct {
		pattern, typ string
		want         bool
	}{
		{"*", "audio.volume.set", true},
		{"audio.*", "audio.volume.set", true},
		{"audio.*", "audio.mute", true},
		{"audio.*", "audio", false},
		{"audio.*", "audiox.volume", false},
		{"audio.*", "media.pause", false},
		{"audio.volume.set", "audio.volume.set", true},
		{"audio.volume.set", "audio.volume.setx", false},
		{"audio.volume.*", "audio.volume.set", true},
		{"audio.volume.*", "audio.mute.set", false},
		{"instance.*", "instance.started", true},
	} {
		if got := MatchType(tc.pattern, tc.typ); got != tc.want {
			t.Errorf("MatchType(%q, %q) = %v, want %v", tc.pattern, tc.typ, got, tc.want)
		}
	}
}

func TestPatternsOverlap(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"app.*", "instance.*", false},
		{"app.*", "apps.*", false},
		{"app.*", "app.*", true},
		{"app.*", "app.start", true},
		{"app.start", "app.*", true},
		{"audio.*", "audio.volume.*", true},
		{"audio.volume.*", "audio.*", true},
		{"audio.volume.*", "audio.mute.*", false},
		{"app.start", "app.stop", false},
		{"app.start", "app.start", true},
		{"*", "anything.at.all", true},
		{"window.*", "instance.*", false},
	} {
		if got := PatternsOverlap(tc.a, tc.b); got != tc.want {
			t.Errorf("PatternsOverlap(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}
