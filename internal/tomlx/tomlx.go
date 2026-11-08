// Package tomlx decodes the TOML files people write by hand: strictly, so a
// typo in a key is an error naming it, never silently ignored.
package tomlx

import (
	"fmt"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// Decode decodes data into v and refuses keys v has no field for. The error
// lists every unknown key, sorted: "unknown key(s): fulscreen, runer.type".
func Decode(data []byte, v any) (toml.MetaData, error) {
	md, err := toml.Decode(string(data), v)
	if err != nil {
		return md, err
	}
	if err := Unknown(md); err != nil {
		return md, err
	}
	return md, nil
}

// Unknown returns the "unknown key(s)" error for what md left undecoded, or
// nil. It is for decoders that must look at the metadata themselves first.
func Unknown(md toml.MetaData) error {
	und := md.Undecoded()
	if len(und) == 0 {
		return nil
	}
	keys := make([]string, len(und))
	for i, k := range und {
		keys[i] = k.String()
	}
	sort.Strings(keys)
	return fmt.Errorf("unknown key(s): %s", strings.Join(keys, ", "))
}
