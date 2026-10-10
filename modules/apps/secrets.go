package apps

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/davitizhgenti/hostd/sdk"
)

// Secrets are values an app's environment names as {secret:<name>}, kept
// out of app files (and any config repository). Each is a 0600 file in
// the secrets directory, unencrypted: this protects against other local
// users and against committing them, not against anything running as the
// screen user or someone holding the disk.

var secretName = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,63}$`)

var secretRef = regexp.MustCompile(`\{secret:([^}]*)\}`)

// DefaultSecretsDir is $XDG_CONFIG_HOME/hostd/secrets.
func DefaultSecretsDir() string {
	return filepath.Join(filepath.Dir(DefaultAppsDir()), "secrets")
}

// SecretList is the secrets' names (never their values).
type SecretList struct {
	Secrets []string `json:"secrets"`
}

func secretActions() []sdk.ActionSpec {
	return []sdk.ActionSpec{
		{Type: "secret.set", Description: "Store a secret, for app environments as {secret:<name>} (the value is read from standard input)",
			Scope:  sdk.ScopeAdmin,
			Schema: json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"},"value":{"type":"string","maxLength":65536}},"required":["name","value"],"additionalProperties":false}`),
			Secret: []string{"value"}, Keys: []sdk.KeyTemplate{"secret:{name}"}},
		{Type: "secret.remove", Description: "Remove a secret",
			Scope:  sdk.ScopeAdmin,
			Schema: json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"}},"required":["name"],"additionalProperties":false}`),
			Keys:   []sdk.KeyTemplate{"secret:{name}"}},
	}
}

func (m *Module) secretPath(name string) (string, error) {
	if !secretName.MatchString(name) {
		return "", sdk.Errorf(sdk.CodeInvalidArgs, "secret name %q: use lowercase letters, digits, '.', '_' and '-'", name)
	}
	if m.opts.SecretsDir == "" {
		return "", sdk.Errorf(sdk.CodeModuleUnavailable, "no secrets directory")
	}
	return filepath.Join(m.opts.SecretsDir, name), nil
}

func (m *Module) handleSecret(a sdk.Action) (sdk.Result, error) {
	var args struct{ Name, Value string }
	if err := a.DecodeArgs(&args); err != nil {
		return sdk.Result{}, err
	}
	path, err := m.secretPath(args.Name)
	if err != nil {
		return sdk.Result{}, err
	}
	if a.Type == "secret.remove" {
		if err := os.Remove(path); errors.Is(err, fs.ErrNotExist) {
			return sdk.Result{}, sdk.Errorf(sdk.CodeNotFound, "no secret %q", args.Name)
		} else if err != nil {
			return sdk.Result{}, err
		}
		return sdk.Result{}, nil
	}
	if err := os.MkdirAll(m.opts.SecretsDir, 0o700); err != nil {
		return sdk.Result{}, err
	}
	tmp, err := os.CreateTemp(m.opts.SecretsDir, ".tmp-*") // 0600
	if err != nil {
		return sdk.Result{}, err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(args.Value); err != nil {
		tmp.Close()
		return sdk.Result{}, err
	}
	if err := tmp.Close(); err != nil {
		return sdk.Result{}, err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return sdk.Result{}, err
	}
	return sdk.Result{Data: sdk.MustJSON(map[string]string{"name": args.Name})}, nil
}

func (m *Module) readSecrets() (SecretList, error) {
	list := SecretList{Secrets: []string{}}
	entries, err := os.ReadDir(m.opts.SecretsDir)
	if errors.Is(err, fs.ErrNotExist) || m.opts.SecretsDir == "" {
		return list, nil
	}
	if err != nil {
		return list, err
	}
	for _, e := range entries {
		if e.Type().IsRegular() && secretName.MatchString(e.Name()) {
			list.Secrets = append(list.Secrets, e.Name())
		}
	}
	sort.Strings(list.Secrets)
	return list, nil
}

// withSecrets returns app with {secret:<name>} in its environment
// replaced by the values (a copy: the catalog keeps the references).
func (m *Module) withSecrets(app *App) (*App, error) {
	need := false
	for _, v := range app.Env {
		if strings.Contains(v, "{secret:") {
			need = true
		}
	}
	if !need {
		return app, nil
	}
	cp := *app
	cp.Env = make(map[string]string, len(app.Env))
	for k, v := range app.Env {
		var missing error
		cp.Env[k] = secretRef.ReplaceAllStringFunc(v, func(ref string) string {
			name := secretRef.FindStringSubmatch(ref)[1]
			path, err := m.secretPath(name)
			if err == nil {
				var b []byte
				if b, err = os.ReadFile(path); err == nil {
					return string(b)
				}
				if errors.Is(err, fs.ErrNotExist) {
					err = sdk.Errorf(sdk.CodeNotFound, "%s needs the secret %q (%s): hostctl secret set %s", app.ID, name, k, name)
				}
			}
			if missing == nil {
				missing = err
			}
			return ""
		})
		if missing != nil {
			return nil, missing
		}
	}
	return &cp, nil
}
