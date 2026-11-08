package sdk

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"
)

// SchemaOf returns the JSON Schema of the Go type T, so a module declares
// the arguments of an action (or the settings of a module, or the result of
// a read) once, as a struct, instead of as a second copy in a string.
//
// Fields follow encoding/json: the name comes from the json tag, a field
// tagged "-" is left out, and a field is required unless it is a pointer or
// its tag says omitempty or omitzero. More keys in the same tags set
// constraints, the way the hand-written schemas do:
//
//	type VolumeSet struct {
//		Percent int    `json:"percent" desc:"0-150" min:"0" max:"150"`
//		Target  string `json:"target,omitempty" enum:"sink,source" pattern:"^[a-z]+$"`
//		Body    string `json:"body,omitempty" media:"text/plain"`
//	}
//
//	desc     description shown by hostctl and in the OpenAPI document
//	min,max  numeric bounds (minLength/maxLength for strings, minItems/maxItems for lists)
//	enum     comma-separated allowed values
//	pattern  regular expression a string must match
//	format   JSON Schema format, such as uri
//	media    contentMediaType: hostctl reads such an argument from @file or "-"
//
// Objects reject unknown properties. A map[string]T is an object with
// additional properties of T; sdk.Duration is a string such as "30s";
// json.RawMessage and any accept any value. SchemaOf panics on a type it
// cannot describe (a channel, a function, a map with non-string keys): that
// is a mistake in the module, found the first time it runs.
func SchemaOf[T any]() json.RawMessage {
	var zero T
	t := reflect.TypeOf(&zero).Elem()
	var buf bytes.Buffer
	if err := writeSchema(&buf, t, nil, nil); err != nil {
		panic(fmt.Sprintf("sdk.SchemaOf[%s]: %v", t, err))
	}
	return json.RawMessage(buf.Bytes())
}

var (
	durationType = reflect.TypeOf(Duration(0))
	timeType     = reflect.TypeOf(time.Time{})
	rawType      = reflect.TypeOf(json.RawMessage(nil))
)

// writeSchema writes the schema of t, with the constraints from a field's
// tags (tag is nil for a root type), and the types being written (to refuse
// a type that contains itself).
func writeSchema(buf *bytes.Buffer, t reflect.Type, tag *reflect.StructTag, stack []reflect.Type) error {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	for _, s := range stack {
		if s == t {
			return fmt.Errorf("type %s contains itself", t)
		}
	}
	kv := newKeys(buf)
	switch {
	case t == durationType:
		kv.raw("type", `"string"`)
	case t == timeType:
		kv.raw("type", `"string"`)
		kv.raw("format", `"date-time"`)
	case t == rawType || t.Kind() == reflect.Interface:
		// any value
	default:
		switch t.Kind() {
		case reflect.String:
			kv.raw("type", `"string"`)
		case reflect.Bool:
			kv.raw("type", `"boolean"`)
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
			reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			kv.raw("type", `"integer"`)
		case reflect.Float32, reflect.Float64:
			kv.raw("type", `"number"`)
		case reflect.Slice, reflect.Array:
			kv.raw("type", `"array"`)
			kv.add("items", func() error { return writeSchema(buf, t.Elem(), nil, append(stack, t)) })
		case reflect.Map:
			if t.Key().Kind() != reflect.String {
				return fmt.Errorf("map %s: keys must be strings", t)
			}
			kv.raw("type", `"object"`)
			kv.add("additionalProperties", func() error { return writeSchema(buf, t.Elem(), nil, append(stack, t)) })
		case reflect.Struct:
			kv.raw("type", `"object"`)
			fields, err := structFields(t)
			if err != nil {
				return err
			}
			if len(fields) > 0 {
				kv.add("properties", func() error {
					props := newKeys(buf)
					for _, f := range fields {
						f := f
						props.add(f.name, func() error { return writeSchema(buf, f.typ, &f.tag, append(stack, t)) })
					}
					return props.end()
				})
			}
			var required []string
			for _, f := range fields {
				if f.required {
					required = append(required, f.name)
				}
			}
			if len(required) > 0 {
				b, _ := json.Marshal(required)
				kv.raw("required", string(b))
			}
			kv.raw("additionalProperties", "false")
		default:
			return fmt.Errorf("cannot describe %s", t)
		}
	}
	if tag != nil {
		if err := tagConstraints(kv, t, *tag); err != nil {
			return err
		}
	}
	return kv.end()
}

// field is one property of a struct type.
type field struct {
	name     string
	typ      reflect.Type
	tag      reflect.StructTag
	required bool
}

// structFields lists the JSON fields of a struct in declaration order,
// with embedded structs inlined as encoding/json does.
func structFields(t reflect.Type) ([]field, error) {
	var out []field
	seen := map[string]bool{}
	for i := 0; i < t.NumField(); i++ {
		sf := t.Field(i)
		jt := sf.Tag.Get("json")
		if jt == "-" {
			continue
		}
		name, opts, _ := strings.Cut(jt, ",")
		if sf.Anonymous && name == "" {
			et := sf.Type
			for et.Kind() == reflect.Pointer {
				et = et.Elem()
			}
			if et.Kind() == reflect.Struct {
				inner, err := structFields(et)
				if err != nil {
					return nil, err
				}
				for _, f := range inner {
					if seen[f.name] {
						return nil, fmt.Errorf("%s: duplicate property %q", t, f.name)
					}
					seen[f.name] = true
					out = append(out, f)
				}
				continue
			}
		}
		if !sf.IsExported() {
			continue
		}
		if name == "" {
			name = sf.Name
		}
		if seen[name] {
			return nil, fmt.Errorf("%s: duplicate property %q", t, name)
		}
		seen[name] = true
		optional := sf.Type.Kind() == reflect.Pointer
		for _, o := range strings.Split(opts, ",") {
			if o == "omitempty" || o == "omitzero" {
				optional = true
			}
		}
		out = append(out, field{name: name, typ: sf.Type, tag: sf.Tag, required: !optional})
	}
	return out, nil
}

// tagConstraints adds the constraints a field's tags give.
func tagConstraints(kv *keys, t reflect.Type, tag reflect.StructTag) error {
	if d := tag.Get("desc"); d != "" {
		b, _ := json.Marshal(d)
		kv.raw("description", string(b))
	}
	if m := tag.Get("media"); m != "" {
		b, _ := json.Marshal(m)
		kv.raw("contentMediaType", string(b))
	}
	if f := tag.Get("format"); f != "" {
		b, _ := json.Marshal(f)
		kv.raw("format", string(b))
	}
	if p := tag.Get("pattern"); p != "" {
		b, _ := json.Marshal(p)
		kv.raw("pattern", string(b))
	}
	if e := tag.Get("enum"); e != "" {
		vals := strings.Split(e, ",")
		var items []any
		for _, v := range vals {
			switch t.Kind() {
			case reflect.String:
				items = append(items, v)
			case reflect.Bool:
				b, err := strconv.ParseBool(v)
				if err != nil {
					return fmt.Errorf("enum %q: %w", v, err)
				}
				items = append(items, b)
			default:
				n, err := strconv.ParseFloat(v, 64)
				if err != nil {
					return fmt.Errorf("enum %q: %w", v, err)
				}
				items = append(items, n)
			}
		}
		b, _ := json.Marshal(items)
		kv.raw("enum", string(b))
	}
	lo, hi := "minimum", "maximum"
	switch t.Kind() {
	case reflect.String:
		lo, hi = "minLength", "maxLength"
	case reflect.Slice, reflect.Array:
		lo, hi = "minItems", "maxItems"
	}
	for _, c := range []struct{ tag, key string }{{"min", lo}, {"max", hi}} {
		v := tag.Get(c.tag)
		if v == "" {
			continue
		}
		if _, err := strconv.ParseFloat(v, 64); err != nil {
			return fmt.Errorf("%s %q: %w", c.tag, v, err)
		}
		kv.raw(c.key, v)
	}
	return nil
}

// keys writes a JSON object's members in the order they are added, so a
// generated schema lists properties as the struct declares them (hostctl
// takes positional arguments in that order). A key given twice keeps its
// first value.
type keys struct {
	buf  *bytes.Buffer
	n    int
	seen map[string]bool
	err  error
}

func newKeys(buf *bytes.Buffer) *keys {
	buf.WriteByte('{')
	return &keys{buf: buf, seen: map[string]bool{}}
}

func (k *keys) key(name string) bool {
	if k.seen[name] {
		return false
	}
	k.seen[name] = true
	if k.n > 0 {
		k.buf.WriteByte(',')
	}
	k.n++
	b, _ := json.Marshal(name)
	k.buf.Write(b)
	k.buf.WriteByte(':')
	return true
}

func (k *keys) raw(name, value string) {
	if k.key(name) {
		k.buf.WriteString(value)
	}
}

func (k *keys) add(name string, write func() error) {
	if k.err != nil || !k.key(name) {
		return
	}
	k.err = write()
}

func (k *keys) end() error {
	k.buf.WriteByte('}')
	return k.err
}
