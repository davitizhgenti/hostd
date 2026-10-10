package deploy

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/davitizhgenti/hostd/sdk"
)

// Push keys are SSH keys that may only git push to services: their line
// in authorized_keys forces "hostctl git-shell" and allows nothing else
// (restrict: no shell, forwarding or terminal). Each carries the comment
// hostd-push:<name>.

const pushMark = "hostd-push:"

var pushName = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,63}$`)

// PushKey is an authorized push key.
type PushKey struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

func pushKeyActions() []sdk.ActionSpec {
	return []sdk.ActionSpec{
		{Type: "deploy.authorize", Description: "Let an SSH public key git push to services, and nothing else",
			Scope:  sdk.ScopeAdmin,
			Schema: json.RawMessage(`{"type":"object","properties":{"name":{"type":"string","description":"who pushes with it, e.g. laptop"},"key":{"type":"string","description":"the public key line (ssh-ed25519 AAAA...)"}},"required":["name","key"],"additionalProperties":false}`),
			Keys:   []sdk.KeyTemplate{"authorized_keys"}},
		{Type: "deploy.revoke", Description: "Remove a push key",
			Scope:  sdk.ScopeAdmin,
			Schema: json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"}},"required":["name"],"additionalProperties":false}`),
			Keys:   []sdk.KeyTemplate{"authorized_keys"}},
	}
}

// parseKey returns a public key line's type and base64 body.
func parseKey(line string) (typ, body string, err error) {
	f := strings.Fields(line)
	bad := sdk.Errorf(sdk.CodeInvalidArgs, "not an SSH public key line (ssh-ed25519 AAAA... comment)")
	for len(f) > 0 && !keyType(f[0]) {
		f = f[1:] // options in front, from a copied authorized_keys line
	}
	if len(f) < 2 {
		return "", "", bad
	}
	if _, err := base64.StdEncoding.DecodeString(f[1]); err != nil {
		return "", "", bad
	}
	return f[0], f[1], nil
}

func keyType(s string) bool {
	return s == "ssh-ed25519" || s == "ssh-rsa" || strings.HasPrefix(s, "ecdsa-sha2-") || strings.HasPrefix(s, "sk-")
}

// authorize adds or replaces name's push key.
func (m *Module) authorize(name, key string) (PushKey, error) {
	if !pushName.MatchString(name) {
		return PushKey{}, sdk.Errorf(sdk.CodeInvalidArgs, "name %q: use lowercase letters, digits, '.', '_' and '-'", name)
	}
	if strings.ContainsAny(key, "\n\r\"") {
		return PushKey{}, sdk.Errorf(sdk.CodeInvalidArgs, "a public key is one line, without quotes")
	}
	typ, body, err := parseKey(key)
	if err != nil {
		return PushKey{}, err
	}
	lines, err := m.authorizedLines()
	if err != nil {
		return PushKey{}, err
	}
	var out []string
	for _, l := range lines {
		_, b, perr := parseKey(l)
		mine := strings.Contains(l, pushMark)
		if perr == nil && b == body && !mine {
			// sshd uses the first line with the key: an unrestricted one
			// would win, and removing it would lock its owner out.
			return PushKey{}, sdk.Errorf(sdk.CodePreconditionFailed, "this key already has full SSH access to the machine; push with a key of its own (ssh-keygen -t ed25519 -f ~/.ssh/hostd-push)")
		}
		if mine && (b == body || strings.HasSuffix(l, " "+pushMark+name)) {
			continue // replaced
		}
		out = append(out, l)
	}
	out = append(out, `command="`+m.opts.GitShell+`",restrict `+typ+" "+body+" "+pushMark+name)
	return PushKey{Name: name, Type: typ}, m.writeAuthorized(out)
}

func (m *Module) revoke(name string) error {
	lines, err := m.authorizedLines()
	if err != nil {
		return err
	}
	var out []string
	found := false
	for _, l := range lines {
		if strings.Contains(l, pushMark) && strings.HasSuffix(l, " "+pushMark+name) {
			found = true
			continue
		}
		out = append(out, l)
	}
	if !found {
		return sdk.Errorf(sdk.CodeNotFound, "no push key %q", name)
	}
	return m.writeAuthorized(out)
}

func (m *Module) pushKeys() ([]PushKey, error) {
	lines, err := m.authorizedLines()
	if err != nil {
		return nil, err
	}
	keys := []PushKey{}
	for _, l := range lines {
		if i := strings.LastIndex(l, " "+pushMark); i >= 0 {
			typ, _, _ := parseKey(l)
			keys = append(keys, PushKey{Name: l[i+1+len(pushMark):], Type: typ})
		}
	}
	return keys, nil
}

func (m *Module) authorizedLines() ([]string, error) {
	b, err := os.ReadFile(m.opts.AuthorizedKeys)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	text := strings.TrimRight(string(b), "\n")
	if text == "" {
		return nil, nil
	}
	return strings.Split(text, "\n"), nil
}

// writeAuthorized replaces authorized_keys in one step (sshd may read it
// at any moment), 0600 in a 0700 directory as sshd requires.
func (m *Module) writeAuthorized(lines []string) error {
	dir := filepath.Dir(m.opts.AuthorizedKeys)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".authorized_keys-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	text := strings.Join(lines, "\n")
	if text != "" {
		text += "\n"
	}
	if _, err := tmp.WriteString(text); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), m.opts.AuthorizedKeys)
}
