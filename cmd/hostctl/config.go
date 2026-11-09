package main

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"

	"github.com/davitizhgenti/hostd/internal/fsx"

	"github.com/BurntSushi/toml"
)

// Config is ~/.config/hostctl/config.toml: one login (the default), and
// any number of named ones, for the machines and the users of a machine that
// each have a hostd (hostctl login ADDRESS --name alice).
type Config struct {
	URL   string `toml:"url"`
	Token string `toml:"token"`
	// CAFingerprint pins the machine's CA (hostctl login saves it): over
	// HTTPS, hostctl trusts that CA and nothing else.
	CAFingerprint string `toml:"ca_fingerprint,omitempty"`

	// Current is the context used when none is asked for (hostctl context
	// NAME sets it); empty is the login above.
	Current  string             `toml:"current,omitempty"`
	Contexts map[string]Context `toml:"contexts,omitempty"`
}

// Context is a named login.
type Context struct {
	URL           string `toml:"url"`
	Token         string `toml:"token"`
	CAFingerprint string `toml:"ca_fingerprint,omitempty"`
}

var reContext = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._@-]{0,63}$`)

func configPath() (string, error) {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "hostctl", "config.toml"), nil
}

// readConfig reads the config file as it is.
func readConfig() (Config, error) {
	var c Config
	path, err := configPath()
	if err != nil {
		return c, err
	}
	if _, err := toml.DecodeFile(path, &c); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return c, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

// loadConfig reads the config file and picks the login to use: the context
// asked for (--context, HOSTD_CONTEXT), else the current one, else the
// default. Then HOSTD_URL, HOSTD_TOKEN and HOSTD_CA_FINGERPRINT override it
// (scripts run by hostd get the first two). Without an address, it falls
// back to the local socket.
func loadConfig(context string) (Config, error) {
	c, err := readConfig()
	if err != nil {
		return c, err
	}
	if context == "" {
		context = os.Getenv("HOSTD_CONTEXT")
	}
	if context == "" {
		context = c.Current
	}
	if context != "" && context != "default" { // "default" is the login saved without a name
		x, ok := c.Contexts[context]
		if !ok {
			return c, usageError{fmt.Errorf("no context %q (hostctl context lists them)", context)}
		}
		c = Config{URL: x.URL, Token: x.Token, CAFingerprint: x.CAFingerprint}
	} else if context == "default" {
		c.Current = ""
	}
	if v := os.Getenv("HOSTD_URL"); v != "" {
		c.URL = v
	}
	if v := os.Getenv("HOSTD_TOKEN"); v != "" {
		c.Token = v
	}
	if v := os.Getenv("HOSTD_CA_FINGERPRINT"); v != "" {
		c.CAFingerprint = v
	}
	if c.URL == "" {
		if rt := os.Getenv("XDG_RUNTIME_DIR"); rt != "" {
			c.URL = "unix://" + filepath.Join(rt, "hostd.sock")
		}
	}
	return c, nil
}

// saveLogin saves a login: as the default one (name empty), or as a
// context, which becomes the current one. Everything else in the file stays.
func saveLogin(name string, x Context) (string, error) {
	c, err := readConfig()
	if err != nil {
		return "", err
	}
	if name == "" {
		c.URL, c.Token, c.CAFingerprint, c.Current = x.URL, x.Token, x.CAFingerprint, ""
	} else {
		if !reContext.MatchString(name) {
			return "", usageError{fmt.Errorf("%q cannot name a context: letters, digits, '.', '_', '@' and '-'", name)}
		}
		if c.Contexts == nil {
			c.Contexts = map[string]Context{}
		}
		c.Contexts[name], c.Current = x, name
	}
	return saveConfig(c)
}

// saveConfig writes the config with mode 0600, since it holds a token.
func saveConfig(c Config) (string, error) {
	path, err := configPath()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(c); err != nil {
		return "", err
	}
	return path, fsx.WriteFile(path, buf.Bytes(), 0o600)
}
