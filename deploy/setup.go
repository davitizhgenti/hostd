// Package deploy is hostd's installer, run as hostd-setup: it turns a fresh
// Debian 13 (trixie) machine into a hostd machine, keeps it up to date and
// can take hostd off it again. It carries what it installs (the Sway, mako
// and greetd configs, the units, the menu, the rollback script) and the
// add-ons, so a signed hostd-setup binary is the whole installer.
//
// Every step checks first and changes only what is missing or different,
// so running it again is safe. Steps work on a Root ("/" on a machine, a
// temporary directory in tests) and run commands through a Runner, so each
// one can be tested without a machine.
package deploy

import (
	"embed"
	"io/fs"
)

// The files the installer puts on the machine, and the add-ons.
//
//go:embed files addons
var embedded embed.FS

// releaseKey is the Ed25519 public key builds are signed with (CI holds
// the private half). The bootstrap install.sh carries the same key.
//
//go:embed release-key.pem
var releaseKey []byte

// Files returns the installer's own files (files/...).
func Files() fs.FS {
	sub, _ := fs.Sub(embedded, "files")
	return sub
}

// file reads one of the installer's files, which are compiled in: a
// missing one is a bug, not a condition to handle.
func file(name string) []byte {
	b, err := fs.ReadFile(Files(), name)
	if err != nil {
		panic("deploy: no embedded file " + name)
	}
	return b
}
