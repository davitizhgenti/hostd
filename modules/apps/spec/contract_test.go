package spec

import (
	"reflect"
	"strings"
	"testing"

	"github.com/davitizhgenti/hostd/contract"
)

// The apps module's Instance is what it writes in instance events and the
// instances read; contract.Instance is what other modules decode from them.
// Every field of the contract must be there, under the same JSON name.
func TestInstanceCarriesTheContract(t *testing.T) {
	ours := jsonFields(reflect.TypeFor[Instance]())
	for name, typ := range jsonFields(reflect.TypeFor[contract.Instance]()) {
		got, ok := ours[name]
		if !ok {
			t.Errorf("contract.Instance's %q is not in spec.Instance", name)
			continue
		}
		for typ.Kind() == reflect.Pointer {
			typ = typ.Elem()
		}
		for got.Kind() == reflect.Pointer {
			got = got.Elem()
		}
		if got.Kind() != typ.Kind() {
			t.Errorf("%q: %s here, %s in the contract", name, got, typ)
		}
	}
}

func jsonFields(t reflect.Type) map[string]reflect.Type {
	out := map[string]reflect.Type{}
	for f := range t.Fields() {
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name != "" && name != "-" {
			out[name] = f.Type
		}
	}
	return out
}
