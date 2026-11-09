package contract

import (
	"context"
	"encoding/json"

	"github.com/davitizhgenti/hostd/sdk"
)

// Deploy is a service's [source]: where new versions come from, how they
// are built, and the public port hostd serves them on. The apps module
// reads it from the app file; the deploy module acts on it.
type Deploy struct {
	Type   string   `json:"type" toml:"type"`               // push: git push to the box; remote: hostd polls url
	Build  []string `json:"build,omitempty" toml:"build"`   // run in the release's directory
	Port   int      `json:"port,omitempty" toml:"port"`     // public port; each release gets PORT
	Branch string   `json:"branch,omitempty" toml:"branch"` // default main
	Keep   int      `json:"keep,omitempty" toml:"keep"`     // releases kept (default 5)
	URL    string   `json:"url,omitempty" toml:"url"`       // remote: the repository
	Poll   string   `json:"poll,omitempty" toml:"poll"`     // remote: how often to look (default 60s)
	Secret string   `json:"secret,omitempty" toml:"secret"` // webhook: the secret's name (hostctl secret set)
	// image: the image (ghcr.io/me/blog), its tag (default latest), the
	// port the container listens on, and a secret with registry
	// credentials (user:token) for a private image.
	Image         string `json:"image,omitempty" toml:"image"`
	Tag           string `json:"tag,omitempty" toml:"tag"`
	ContainerPort int    `json:"container_port,omitempty" toml:"container_port"`
	Auth          string `json:"auth,omitempty" toml:"auth"`
}

// Service is an app that has a [source]: the part of the catalog the deploy
// module needs.
type Service struct {
	ID     string  `json:"id"`
	Deploy *Deploy `json:"deploy"`
}

// ReadServices asks the apps module for the apps with a [source], by ID.
func ReadServices(ctx context.Context, core sdk.Core) (map[string]Service, error) {
	raw, err := core.Read(ctx, AppsModule, ReadCatalog, map[string]string{"all": "true"})
	if err != nil {
		return nil, err
	}
	var list struct {
		Apps []Service `json:"apps"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, err
	}
	out := map[string]Service{}
	for _, a := range list.Apps {
		if a.Deploy != nil {
			out[a.ID] = a
		}
	}
	return out, nil
}
