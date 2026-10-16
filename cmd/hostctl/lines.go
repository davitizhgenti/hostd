package main

import (
	"encoding/json"
	"regexp"
	"strings"
)

// The lines of the manifest's CLI hints (done, column text, notes):
// "TEXT" or "TEXT if COND". COND is field (set: not empty, false, 0 or
// null), !field (not set), or field=value. In TEXT, {field} is a field,
// {a|b} the first of the two that is set, and {field:time} or
// {field:hm} a time, as the local time of day.

var rePlaceholder = regexp.MustCompile(`\{([a-z0-9_.|]+)(?::(time|hm|date))?\}`)

// renderLines is the first line whose condition holds for data, filled in.
func renderLines(lines []string, data any) (string, bool) {
	for _, line := range lines {
		text, cond, _ := strings.Cut(line, " if ")
		if cond != "" && !holds(cond, data) {
			continue
		}
		return rePlaceholder.ReplaceAllStringFunc(text, func(ph string) string {
			m := rePlaceholder.FindStringSubmatch(ph)
			return formatValue(fieldOf(data, m[1]), m[2])
		}), true
	}
	return "", false
}

func holds(cond string, data any) bool {
	if field, ok := strings.CutPrefix(cond, "!"); ok {
		return !set(fieldOf(data, field))
	}
	if field, want, ok := strings.Cut(cond, "="); ok {
		return render(fieldOf(data, field), "") == want
	}
	return set(fieldOf(data, cond))
}

// set reports whether a value says something: not null, false, 0, "" or
// an empty list or object.
func set(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case json.Number:
		f, err := x.Float64()
		return err != nil || f != 0
	case float64:
		return x != 0
	case []any:
		return len(x) > 0
	case map[string]any:
		return len(x) > 0
	}
	return true
}

// fieldOf is a field of data ("a.b"); "a|b" is the first that is set.
func fieldOf(data any, paths string) any {
	var v any
	for _, path := range strings.Split(paths, "|") {
		v = data
		for _, k := range strings.Split(path, ".") {
			m, _ := v.(map[string]any)
			v = m[k]
		}
		if set(v) {
			return v
		}
	}
	return v
}

func formatValue(v any, format string) string {
	return render(v, format)
}
