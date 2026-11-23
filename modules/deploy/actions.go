package deploy

import (
	"context"
	"os"
	"time"

	"github.com/davitizhgenti/hostd/contract"
	"github.com/davitizhgenti/hostd/sdk"
	"github.com/davitizhgenti/hostd/sdk/kit"
)

// The arguments and results of the module's actions.
type (
	appArgs struct {
		App string `json:"app" desc:"the service (an app with a [source])"`
		Rev string `json:"rev,omitempty" desc:"commit, tag or branch (default: the source's branch)"`
	}
	authorizeArgs struct {
		Name string `json:"name" desc:"who pushes with it, e.g. laptop"`
		Key  string `json:"key" desc:"the public key line (ssh-ed25519 AAAA...)"`
	}
	revokeArgs struct {
		Name string `json:"name"`
	}
	followArgs struct {
		URL    string `json:"url" desc:"the repository"`
		Branch string `json:"branch,omitempty" desc:"default main"`
		Poll   string `json:"poll,omitempty" desc:"how often to look, default 60s"`
	}

	// deployKey is a deploy key's public half, to add to the repository.
	deployKey struct {
		PublicKey string `json:"public_key"`
		Path      string `json:"path"`
	}
	// repo is a service's new repository: where to push, or what it pulls.
	repo struct {
		Repo   string `json:"repo"`
		Branch string `json:"branch"`
		URL    string `json:"url,omitempty"`
		Remote string `json:"remote,omitempty"`
	}
)

// declare is the module's actions.
func (m *Module) declare() *kit.Actions {
	t := kit.NewActions("deploy")
	keys := []sdk.KeyTemplate{"deploy:{app}"}
	kit.On(t, sdk.ActionSpec{Type: "deploy.init", Description: "Create the service's git repository: git push to it deploys",
		Keys: keys, Scope: contract.ScopeDeploy, Timeout: sdk.Duration(30 * time.Second),
		Route: &sdk.Route{Method: "POST", Path: "/v1/deploys/{app}/init"}},
		func(ctx context.Context, _ sdk.Action, args appArgs) (repo, error) {
			s, err := m.service(ctx, args.App)
			if err != nil {
				return repo{}, err
			}
			dir, err := m.initRepo(ctx, s.ID, branchOf(s.Deploy))
			if err != nil {
				return repo{}, err
			}
			m.sync(ctx) // the public port opens now, before the first push
			if s.Deploy.Type != "push" {
				return repo{Repo: dir, Branch: branchOf(s.Deploy), URL: s.Deploy.URL}, nil
			}
			host, _ := os.Hostname()
			return repo{Repo: dir, Branch: branchOf(s.Deploy), Remote: "ssh://" + os.Getenv("USER") + "@" + host + dir}, nil
		})
	kit.On(t, sdk.ActionSpec{Type: "deploy.run", Description: "Build a commit and switch to it with no downtime; on any failure the running release stays",
		Keys: keys, Scope: contract.ScopeDeploy, Timeout: sdk.Duration(20 * time.Minute), Busy: true,
		Route: &sdk.Route{Method: "POST", Path: "/v1/deploys/{app}/run"}},
		func(ctx context.Context, _ sdk.Action, args appArgs) (Release, error) {
			s, err := m.service(ctx, args.App)
			if err != nil {
				return Release{}, err
			}
			return m.run(ctx, s, args.Rev)
		})
	kit.On(t, sdk.ActionSpec{Type: "deploy.key", Description: "The service's deploy key (made once): add its public key to the repository, read-only",
		Keys: keys, Scope: contract.ScopeDeploy, Timeout: sdk.Duration(30 * time.Second),
		Route: &sdk.Route{Method: "POST", Path: "/v1/deploys/{app}/key"}},
		func(ctx context.Context, _ sdk.Action, args appArgs) (deployKey, error) {
			s, err := m.service(ctx, args.App)
			if err != nil {
				return deployKey{}, err
			}
			return m.deployKey(ctx, s.ID)
		})
	kit.On(t, sdk.ActionSpec{Type: "deploy.rollback", Description: "Switch back to the release before the live one",
		Keys: keys, Scope: contract.ScopeDeploy, Timeout: sdk.Duration(10 * time.Minute), Busy: true,
		Route: &sdk.Route{Method: "POST", Path: "/v1/deploys/{app}/rollback"}},
		func(ctx context.Context, _ sdk.Action, args appArgs) (Release, error) {
			s, err := m.service(ctx, args.App)
			if err != nil {
				return Release{}, err
			}
			return m.rollback(ctx, s)
		})

	pushKeys := []sdk.KeyTemplate{"authorized_keys"}
	kit.On(t, sdk.ActionSpec{Type: "deploy.authorize", Description: "Let an SSH public key git push to services, and nothing else",
		Scope: sdk.ScopeAdmin, Keys: pushKeys},
		func(_ context.Context, _ sdk.Action, args authorizeArgs) (PushKey, error) {
			return m.authorize(args.Name, args.Key)
		})
	kit.On(t, sdk.ActionSpec{Type: "deploy.revoke", Description: "Remove a push key",
		Scope: sdk.ScopeAdmin, Keys: pushKeys},
		func(_ context.Context, _ sdk.Action, args revokeArgs) (struct{}, error) {
			return struct{}{}, m.revoke(args.Name)
		})

	config := []sdk.KeyTemplate{"config"}
	kit.On(t, sdk.ActionSpec{Type: "config.follow", Description: "Take the app files from a git repository's apps/ folder, following its branch",
		Scope: sdk.ScopeAdmin, Keys: config, Timeout: sdk.Duration(5 * time.Minute)},
		func(ctx context.Context, _ sdk.Action, args followArgs) (*Follow, error) { return m.follow(ctx, args) })
	kit.On(t, sdk.ActionSpec{Type: "config.unfollow", Description: "Stop following; the app files in use stay, as ordinary files",
		Scope: sdk.ScopeAdmin, Keys: config},
		func(context.Context, sdk.Action, struct{}) (struct{}, error) { return struct{}{}, m.unfollow() })
	kit.On(t, sdk.ActionSpec{Type: "config.sync", Description: "Look at the followed repository now",
		Scope: sdk.ScopeAdmin, Keys: config, Timeout: sdk.Duration(5 * time.Minute)},
		func(ctx context.Context, _ sdk.Action, _ struct{}) (*Follow, error) { return m.configSync(ctx) })
	kit.On(t, sdk.ActionSpec{Type: "config.key", Description: "The followed repository's deploy key (made once): add its public key to the repository, read-only",
		Scope: sdk.ScopeAdmin, Keys: config},
		func(ctx context.Context, _ sdk.Action, _ struct{}) (deployKey, error) {
			return m.deployKey(ctx, configKey)
		})
	return t
}

// deployKey makes the key of a service (or of the followed config) once,
// and returns its public half.
func (m *Module) deployKey(ctx context.Context, name string) (deployKey, error) {
	pub, err := m.makeKey(ctx, name)
	if err != nil {
		return deployKey{}, err
	}
	return deployKey{PublicKey: pub, Path: m.keyPath(name)}, nil
}
