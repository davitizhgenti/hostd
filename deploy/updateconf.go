package deploy

import (
	"bufio"
	"os"
	"strings"
)

// UpdateConf is /etc/hostd/update.conf: which builds the update timer
// looks for, and what it does with a new one.
type UpdateConf struct {
	Channel string // edge (every build of main that passed CI) or stable (releases)
	Apply   string // auto, notify or manual
}

// ReadUpdateConf reads the file at path; what is missing is the default
// (edge, auto).
func ReadUpdateConf(path string) UpdateConf {
	c := UpdateConf{Channel: "edge", Apply: "auto"}
	f, err := os.Open(path)
	if err != nil {
		return c
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line, _, _ := strings.Cut(sc.Text(), "#")
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if f := strings.Fields(v); len(f) > 0 {
			v = f[0]
		} else {
			v = ""
		}
		switch {
		case !ok || v == "":
		case k == "CHANNEL" && (v == "edge" || v == "stable"):
			c.Channel = v
		case k == "APPLY" && (v == "auto" || v == "notify" || v == "manual"):
			c.Apply = v
		}
	}
	return c
}

// ReleaseURL is where a channel's builds are published.
func (c UpdateConf) ReleaseURL() string {
	if c.Channel == "stable" {
		return "https://github.com/davitizhgenti/hostd/releases/latest/download"
	}
	return "https://github.com/davitizhgenti/hostd/releases/download/edge"
}
