package sdk

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestKeyTemplateParams(t *testing.T) {
	for _, tc := range []struct {
		tmpl    KeyTemplate
		want    []string
		wantErr string
	}{
		{"audio.master", nil, ""},
		{"instance:{id}", []string{"id"}, ""},
		{"audio.stream:{instance}", []string{"instance"}, ""},
		{"place:{a}:{b}", []string{"a", "b"}, ""},
		{"instance:{id", nil, "unbalanced"},
		{"instance:id}", nil, "unbalanced"},
		{"instance:{}", nil, "bad placeholder"},
		{"instance:{Id}", nil, "bad placeholder"},
		{"instance:{1d}", nil, "bad placeholder"},
	} {
		got, err := tc.tmpl.Params()
		if tc.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("%q: err = %v, want %q", tc.tmpl, err, tc.wantErr)
			}
			continue
		}
		if err != nil || !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%q: Params() = %v, %v; want %v", tc.tmpl, got, err, tc.want)
		}
	}
}

func TestExpandKeys(t *testing.T) {
	for _, tc := range []struct {
		name      string
		templates []KeyTemplate
		args      string
		want      []string
		wantCode  Code
	}{
		{"no templates", nil, `{"id":"x"}`, nil, ""},
		{"fixed key", []KeyTemplate{"audio.master"}, ``, []string{"audio.master"}, ""},
		{"string arg", []KeyTemplate{"instance:{id}"}, `{"id":"dota2#1"}`, []string{"instance:dota2#1"}, ""},
		{"number arg", []KeyTemplate{"output:{n}"}, `{"n":3}`, []string{"output:3"}, ""},
		{
			"sorted for lock order",
			[]KeyTemplate{"instance:{id}", "display.focus"},
			`{"id":"firefox"}`,
			[]string{"display.focus", "instance:firefox"},
			"",
		},
		{
			"duplicates removed",
			[]KeyTemplate{"instance:{a}", "instance:{b}"},
			`{"a":"x","b":"x"}`,
			[]string{"instance:x"},
			"",
		},
		{"missing arg", []KeyTemplate{"instance:{id}"}, `{}`, nil, CodeInvalidArgs},
		{"null arg", []KeyTemplate{"instance:{id}"}, `{"id":null}`, nil, CodeInvalidArgs},
		{"empty string", []KeyTemplate{"instance:{id}"}, `{"id":""}`, nil, CodeInvalidArgs},
		{"bool arg", []KeyTemplate{"instance:{id}"}, `{"id":true}`, nil, CodeInvalidArgs},
		{"object arg", []KeyTemplate{"instance:{id}"}, `{"id":{"x":1}}`, nil, CodeInvalidArgs},
		{"args not an object", []KeyTemplate{"instance:{id}"}, `[1]`, nil, CodeInvalidArgs},
		{"broken template", []KeyTemplate{"instance:{id"}, `{"id":"x"}`, nil, CodeInternal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ExpandKeys(tc.templates, json.RawMessage(tc.args))
			if tc.wantCode != "" {
				if CodeOf(err) != tc.wantCode {
					t.Fatalf("err = %v, want code %s", err, tc.wantCode)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ExpandKeys = %v, %v; want %v", got, err, tc.want)
			}
		})
	}
}
