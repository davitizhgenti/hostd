package sdk

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// KeyTemplate is a resource key with argument placeholders, such as
// "instance:{id}" or "audio.stream:{instance}". Placeholders name top-level
// arguments whose values are strings or numbers.
type KeyTemplate string

// Params returns the argument names the template refers to, in order.
func (t KeyTemplate) Params() ([]string, error) {
	var params []string
	s := string(t)
	for {
		open := strings.IndexByte(s, '{')
		closing := strings.IndexByte(s, '}')
		switch {
		case open < 0 && closing < 0:
			return params, nil
		case open < 0 || closing < open:
			return nil, fmt.Errorf("key template %q: unbalanced braces", t)
		}
		name := s[open+1 : closing]
		if !isIdent(name) {
			return nil, fmt.Errorf("key template %q: bad placeholder {%s}", t, name)
		}
		params = append(params, name)
		s = s[closing+1:]
	}
}

// ExpandKeys fills each template from args and returns the keys sorted and
// without duplicates, the order in which the core acquires them. A missing
// or non-scalar argument is an invalid_args error.
func ExpandKeys(templates []KeyTemplate, args json.RawMessage) ([]string, error) {
	if len(templates) == 0 {
		return nil, nil
	}
	values := map[string]json.RawMessage{}
	if len(bytes.TrimSpace(args)) > 0 {
		if err := json.Unmarshal(args, &values); err != nil {
			return nil, Errorf(CodeInvalidArgs, "arguments must be a JSON object")
		}
	}
	seen := map[string]bool{}
	keys := make([]string, 0, len(templates))
	for _, t := range templates {
		params, err := t.Params()
		if err != nil {
			return nil, Errorf(CodeInternal, "%v", err)
		}
		key := string(t)
		for _, p := range params {
			v, err := scalar(values[p])
			if err != nil {
				return nil, Errorf(CodeInvalidArgs, "argument %q: %v", p, err)
			}
			key = strings.Replace(key, "{"+p+"}", v, 1)
		}
		if !seen[key] {
			seen[key] = true
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys, nil
}

// scalar renders a JSON string or number for use inside a key.
func scalar(raw json.RawMessage) (string, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return "", fmt.Errorf("required")
	}
	switch raw[0] {
	case '"':
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return "", err
		}
		if s == "" {
			return "", fmt.Errorf("must not be empty")
		}
		return s, nil
	case '-', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
		var n json.Number
		if err := json.Unmarshal(raw, &n); err != nil {
			return "", err
		}
		return n.String(), nil
	default:
		return "", fmt.Errorf("must be a string or number")
	}
}

func isIdent(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r == '_':
		case (r >= '0' && r <= '9') && i > 0:
		default:
			return false
		}
	}
	return true
}
