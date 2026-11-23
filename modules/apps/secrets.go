package apps

import (
	"context"
	"errors"
	"regexp"
	"strings"

	"github.com/davitizhgenti/hostd/internal/secrets"
	"github.com/davitizhgenti/hostd/sdk"
)

// Secrets are values an app's environment names as {secret:<name>}; the
// store itself is internal/secrets, shared with the deploy module.

var secretRef = regexp.MustCompile(`\{secret:([^}]*)\}`)

// SecretList is the secrets' names (never their values).
type SecretList struct {
	Secrets []string `json:"secrets"`
}

// secretName checks a secret's name, or says why it is not one.
func (m *Module) secretName(name string) error {
	if !secrets.ValidName(name) {
		return sdk.Errorf(sdk.CodeInvalidArgs, "secret name %q: use lowercase letters, digits, '.', '_' and '-'", name)
	}
	if m.opts.SecretsDir == "" {
		return sdk.Errorf(sdk.CodeModuleUnavailable, "no secrets directory")
	}
	return nil
}

func (m *Module) setSecret(_ context.Context, _ sdk.Action, args secretArgs) (secretName, error) {
	if err := m.secretName(args.Name); err != nil {
		return secretName{}, err
	}
	if err := (secrets.Store{Dir: m.opts.SecretsDir}).Write(args.Name, []byte(args.Value)); err != nil {
		return secretName{}, err
	}
	return secretName{Name: args.Name}, nil
}

func (m *Module) removeSecret(_ context.Context, _ sdk.Action, args secretName) (struct{}, error) {
	if err := m.secretName(args.Name); err != nil {
		return struct{}{}, err
	}
	if err := (secrets.Store{Dir: m.opts.SecretsDir}).Remove(args.Name); errors.Is(err, secrets.ErrNotExist) {
		return struct{}{}, sdk.Errorf(sdk.CodeNotFound, "no secret %q", args.Name)
	} else if err != nil {
		return struct{}{}, err
	}
	return struct{}{}, nil
}

func (m *Module) readSecrets() (SecretList, error) {
	names, err := secrets.Store{Dir: m.opts.SecretsDir}.Names()
	return SecretList{Secrets: names}, err
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
			err := m.secretName(name)
			if err == nil {
				var b []byte
				if b, err = (secrets.Store{Dir: m.opts.SecretsDir}).Read(name); err == nil {
					return string(b)
				}
				if errors.Is(err, secrets.ErrNotExist) {
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
