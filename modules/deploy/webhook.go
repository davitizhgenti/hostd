package deploy

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/davitizhgenti/hostd/sdk"
)

// A webhook source is told about pushes by the git host (GitHub, Gitea,
// Forgejo): POST /v1/hooks/git/<app>, signed with the webhook's secret
// (HMAC-SHA256 of the body). The request carries no token, so the
// signature is the only proof it comes from the host. The commit is then
// fetched from the source's url, as for a remote source.

var secretName = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,63}$`)

// seenDeliveries is how many delivery IDs are remembered, to ignore a
// request sent again (a replay).
const seenDeliveries = 256

func (m *Module) Hook(ctx context.Context, name string, req sdk.HookRequest) (any, error) {
	if name != "git" {
		return nil, sdk.Errorf(sdk.CodeNotFound, "deploy module has no hook %q", name)
	}
	h := http.Header(req.Header)
	app := req.Params["app"]
	s, err := m.service(ctx, app)
	if err != nil || s.Deploy.Type != "webhook" {
		return nil, sdk.Errorf(sdk.CodeNotFound, "%q is not a service with a webhook source", app)
	}
	secret, err := m.readSecret(s.Deploy.Secret)
	if err != nil {
		return nil, err
	}
	if !signed(h, req.Body, secret) {
		return nil, sdk.Errorf(sdk.CodeUnauthorized, "the request's signature does not match the webhook secret")
	}
	if id := first(h, "X-GitHub-Delivery", "X-Gitea-Delivery"); id != "" && m.seen(id) {
		return map[string]string{"status": "ignored", "reason": "this delivery was already handled"}, nil
	}
	switch first(h, "X-GitHub-Event", "X-Gitea-Event") {
	case "ping":
		return map[string]string{"status": "pong"}, nil
	case "push":
	default:
		return map[string]string{"status": "ignored", "reason": "not a push"}, nil
	}
	var push struct {
		Ref   string `json:"ref"`
		After string `json:"after"`
	}
	if err := json.Unmarshal(req.Body, &push); err != nil {
		return nil, sdk.Errorf(sdk.CodeInvalidArgs, "push event: %v", err)
	}
	if push.Ref != "refs/heads/"+s.branch() {
		return map[string]string{"status": "ignored", "reason": "a push to " + push.Ref + ", not " + s.branch()}, nil
	}
	if strings.Trim(push.After, "0") == "" {
		return map[string]string{"status": "ignored", "reason": "the branch was deleted"}, nil
	}
	if m.inHistory(s.ID, push.After) {
		return map[string]string{"status": "ignored", "reason": "this commit was deployed before"}, nil
	}
	if !m.deployInBackground(s, push.After) {
		return map[string]string{"status": "ignored", "reason": "a deploy of this service is already running"}, nil
	}
	return map[string]string{"status": "deploying", "rev": push.After}, nil
}

// signed checks GitHub's X-Hub-Signature-256 (sha256=<hex>) or Gitea's
// X-Gitea-Signature (<hex>).
func signed(h http.Header, body []byte, secret []byte) bool {
	got := h.Get("X-Hub-Signature-256")
	if got != "" {
		var ok bool
		if got, ok = strings.CutPrefix(got, "sha256="); !ok {
			return false
		}
	} else {
		got = h.Get("X-Gitea-Signature")
	}
	sig, err := hex.DecodeString(got)
	if err != nil || len(sig) == 0 {
		return false
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return hmac.Equal(sig, mac.Sum(nil))
}

func first(h http.Header, names ...string) string {
	for _, n := range names {
		if v := h.Get(n); v != "" {
			return v
		}
	}
	return ""
}

// seen records a delivery ID and reports whether it came before.
func (m *Module) seen(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, d := range m.deliveries {
		if d == id {
			return true
		}
	}
	m.deliveries = append(m.deliveries, id)
	if len(m.deliveries) > seenDeliveries {
		m.deliveries = m.deliveries[1:]
	}
	return false
}

func (m *Module) readSecret(name string) ([]byte, error) {
	if !secretName.MatchString(name) || m.opts.SecretsDir == "" {
		return nil, sdk.Errorf(sdk.CodeModuleUnavailable, "the webhook secret %q cannot be read", name)
	}
	b, err := os.ReadFile(filepath.Join(m.opts.SecretsDir, name))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, sdk.Errorf(sdk.CodeModuleUnavailable, "the webhook secret %q is not set: hostctl secret set %s", name, name)
	}
	if err != nil {
		return nil, err
	}
	if len(b) == 0 {
		return nil, sdk.Errorf(sdk.CodeModuleUnavailable, "the webhook secret %q is empty", name)
	}
	return b, nil
}
