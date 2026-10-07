package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// Config is ~/.config/hostctl/config.toml.
type Config struct {
	URL   string `toml:"url"`
	Token string `toml:"token"`
}

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

// loadConfig reads the config file, then lets HOSTD_URL and HOSTD_TOKEN
// override it (scripts run by hostd get those). Without either, it falls
// back to the local socket.
func loadConfig() (Config, error) {
	var c Config
	path, err := configPath()
	if err != nil {
		return c, err
	}
	if _, err := toml.DecodeFile(path, &c); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return c, fmt.Errorf("%s: %w", path, err)
	}
	if v := os.Getenv("HOSTD_URL"); v != "" {
		c.URL = v
	}
	if v := os.Getenv("HOSTD_TOKEN"); v != "" {
		c.Token = v
	}
	if c.URL == "" {
		if rt := os.Getenv("XDG_RUNTIME_DIR"); rt != "" {
			c.URL = "unix://" + filepath.Join(rt, "hostd.sock")
		}
	}
	return c, nil
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
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.toml")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return "", err
	}
	if err := toml.NewEncoder(tmp).Encode(c); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	return path, os.Rename(tmp.Name(), path)
}
