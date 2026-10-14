package main

import (
	"reflect"
	"strings"
	"testing"
)

// The command line is the one the shell installer had: scripts and the
// update timer call it so.
func TestParse(t *testing.T) {
	for _, c := range []struct {
		argv []string
		want args
	}{
		{nil, args{cmd: "install"}},
		{[]string{"--gpu", "other", "--from", "/d"}, args{cmd: "install", gpu: "other", from: "/d", pass: []string{"--gpu", "other", "--from", "/d"}}},
		{[]string{"update", "steam", "--verbose", "--source", "/s", "dolphin"},
			args{cmd: "update", source: "/s", verbose: true, names: []string{"steam", "dolphin"}}},
		{[]string{"update", "--all"}, args{cmd: "update", all: true}},
		{[]string{"uninstall", "--purge", "--all"}, args{cmd: "uninstall", purge: true, all: true}},
		{[]string{"add", "controllers", "--user", "tv"}, args{cmd: "add", user: "tv", names: []string{"controllers"}, pass: []string{"--user", "tv"}}},
		{[]string{"auto-update"}, args{cmd: "auto-update"}},
		{[]string{"-h"}, args{cmd: "help"}},
	} {
		got, err := parse(c.argv)
		if err != nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("%v: %+v %v, want %+v", c.argv, got, err, c.want)
		}
	}
	for argv, want := range map[string]string{
		"--gpu amd":      "--gpu must be",
		"--from":         "needs a value",
		"install extra":  "unexpected argument",
		"--purge":        "goes with uninstall",
		"install --all":  "goes with update or uninstall",
		"update --bogus": "unknown option",
	} {
		if _, err := parse(strings.Fields(argv)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v, want %q", argv, err, want)
		}
	}
}
