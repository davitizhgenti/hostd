// Package version reports the build version of hostd and hostctl.
package version

import "strings"

// Version is set at build time with
// -ldflags "-X github.com/davitizhgenti/hostd/internal/version.Version=v0.1.0".
// Builds without it, such as laptop builds pushed with hostctl update push,
// report a dev version.
var Version = "dev"

// IsDev reports whether this is an unreleased development build.
func IsDev() bool { return Version == "dev" || strings.HasSuffix(Version, "-dev") }
