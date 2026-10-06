package sdk

import "strings"

// MatchType reports whether an action or event type matches pattern.
//
// Patterns are an exact type ("audio.volume.set"), a namespace ending in
// ".*" that matches any type below it ("audio.*" matches "audio.volume.set"
// but not "audio" itself), or "*" for everything.
func MatchType(pattern, typ string) bool {
	switch {
	case pattern == "*":
		return true
	case strings.HasSuffix(pattern, ".*"):
		prefix := pattern[:len(pattern)-1] // keep the dot
		return len(typ) > len(prefix) && strings.HasPrefix(typ, prefix)
	default:
		return pattern == typ
	}
}

// PatternsOverlap reports whether some type could match both patterns. The
// core uses it to refuse two modules claiming the same namespace.
func PatternsOverlap(a, b string) bool {
	if a == "*" || b == "*" {
		return true
	}
	aw, bw := strings.HasSuffix(a, ".*"), strings.HasSuffix(b, ".*")
	switch {
	case aw && bw:
		pa, pb := a[:len(a)-1], b[:len(b)-1]
		return strings.HasPrefix(pa, pb) || strings.HasPrefix(pb, pa)
	case aw:
		return MatchType(a, b)
	case bw:
		return MatchType(b, a)
	default:
		return a == b
	}
}
