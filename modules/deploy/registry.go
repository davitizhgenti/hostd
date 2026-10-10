package deploy

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// An image source's tag is looked up in its registry (the OCI
// distribution API): the manifest's digest names the image exactly, so a
// release runs image@digest and a new digest is a new version.

// imageRef splits an image name into its registry host and repository:
// ghcr.io/me/blog, me/app (Docker Hub), nginx (Docker Hub library/).
func imageRef(image string) (host, repo string) {
	first, rest, ok := strings.Cut(image, "/")
	if ok && (strings.ContainsAny(first, ".:") || first == "localhost") {
		return first, rest
	}
	if !ok {
		return "registry-1.docker.io", "library/" + image
	}
	return "registry-1.docker.io", image
}

// registryURL is https, except for a registry on this machine.
func registryURL(host string) string {
	h := host
	if i := strings.LastIndex(h, ":"); i >= 0 {
		h = h[:i]
	}
	if h == "localhost" || h == "127.0.0.1" || h == "::1" || h == "[::1]" {
		return "http://" + host
	}
	return "https://" + host
}

var manifestTypes = strings.Join([]string{
	"application/vnd.oci.image.index.v1+json",
	"application/vnd.docker.distribution.manifest.list.v2+json",
	"application/vnd.oci.image.manifest.v1+json",
	"application/vnd.docker.distribution.manifest.v2+json",
}, ", ")

// digest returns the digest of image:tag. creds is "user:token" (or a
// bare token) for a private image, or empty.
func (m *Module) digest(ctx context.Context, image, tag, creds string) (string, error) {
	host, repo := imageRef(image)
	u := registryURL(host) + "/v2/" + repo + "/manifests/" + url.PathEscape(tag)
	user, pass := "", ""
	if creds != "" {
		var ok bool
		if user, pass, ok = strings.Cut(creds, ":"); !ok {
			user, pass = "hostd", creds
		}
	}
	client := m.opts.HTTP
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	try := func(auth string) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodHead, u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", manifestTypes)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		return client.Do(req)
	}
	res, err := try("")
	if err != nil {
		return "", err
	}
	res.Body.Close()
	if res.StatusCode == http.StatusUnauthorized {
		auth, err := m.registryAuth(ctx, res.Header.Get("WWW-Authenticate"), user, pass)
		if err != nil {
			return "", fmt.Errorf("%s: %w", host, err)
		}
		if res, err = try(auth); err != nil {
			return "", err
		}
		res.Body.Close()
	}
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s:%s: the registry answered %s", image, tag, res.Status)
	}
	d := res.Header.Get("Docker-Content-Digest")
	if !strings.HasPrefix(d, "sha256:") || len(d) != 71 {
		return "", fmt.Errorf("%s:%s: the registry gave no digest", image, tag)
	}
	return d, nil
}

// registryAuth answers a registry's challenge: basic credentials, or a
// bearer token from its token service (anonymous for public images).
func (m *Module) registryAuth(ctx context.Context, challenge, user, pass string) (string, error) {
	scheme, params, _ := strings.Cut(challenge, " ")
	switch strings.ToLower(scheme) {
	case "basic":
		if user == "" {
			return "", fmt.Errorf("the registry needs credentials: a secret named in the source's auth")
		}
		return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass)), nil
	case "bearer":
	default:
		return "", fmt.Errorf("unknown authentication %q", challenge)
	}
	p := map[string]string{}
	for _, kv := range splitParams(params) {
		k, v, _ := strings.Cut(kv, "=")
		p[strings.ToLower(strings.TrimSpace(k))] = strings.Trim(strings.TrimSpace(v), `"`)
	}
	if p["realm"] == "" {
		return "", fmt.Errorf("no token service in %q", challenge)
	}
	q := url.Values{}
	if p["service"] != "" {
		q.Set("service", p["service"])
	}
	if p["scope"] != "" {
		q.Set("scope", p["scope"])
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p["realm"]+"?"+q.Encode(), nil)
	if err != nil {
		return "", err
	}
	if user != "" {
		req.SetBasicAuth(user, pass)
	}
	res, err := m.opts.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token service: %s", res.Status)
	}
	var tok struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&tok); err != nil {
		return "", fmt.Errorf("token service: %w", err)
	}
	if tok.Token == "" {
		tok.Token = tok.AccessToken
	}
	if tok.Token == "" {
		return "", fmt.Errorf("token service gave no token")
	}
	return "Bearer " + tok.Token, nil
}

// splitParams splits a=b,c="d,e" at the commas outside quotes.
func splitParams(s string) []string {
	var out []string
	quoted, start := false, 0
	for i, r := range s {
		switch {
		case r == '"':
			quoted = !quoted
		case r == ',' && !quoted:
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

// imageDigest is the digest of an image source's tag now.
func (m *Module) imageDigest(ctx context.Context, s service) (string, error) {
	tag := s.Deploy.Tag
	if tag == "" {
		tag = "latest"
	}
	var creds string
	if s.Deploy.Auth != "" {
		b, err := m.readSecret(s.Deploy.Auth)
		if err != nil {
			return "", err
		}
		creds = strings.TrimSpace(string(b))
	}
	return m.digest(ctx, s.Deploy.Image, tag, creds)
}
