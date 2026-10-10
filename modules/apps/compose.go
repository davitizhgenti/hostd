package apps

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/davitizhgenti/hostd/sdk"
)

// labelProject is the label compose (Docker Compose, podman-compose) puts
// on a project's containers.
const labelProject = "com.docker.compose.project"

// ComposeRunner runs compose apps: each instance is a compose project,
// hostd-<instance>, brought up with the app's file. Status, stops and
// adoption go through the engine API by the project label, so they need
// neither the file nor the compose tool.
type ComposeRunner struct {
	Docker Docker
	Socket string // the engine socket (DOCKER_HOST for the compose tool)
	// Command runs compose: default podman compose, else docker compose.
	Command []string
	// Run runs the compose tool (tests replace it).
	Run func(ctx context.Context, argv, env []string) error
}

// projectName is reversible: '#' → "__", '.' → "_d", '_' → "_u"
// (project names allow only lowercase letters, digits, '-' and '_').
func projectName(instance string) string {
	r := strings.NewReplacer("_", "_u", ".", "_d", "#", "__")
	return "hostd-" + r.Replace(instance)
}

func instanceOfProject(project string) (string, bool) {
	rest, ok := strings.CutPrefix(project, "hostd-")
	if !ok || rest == "" {
		return "", false
	}
	var b strings.Builder
	for i := 0; i < len(rest); i++ {
		if rest[i] != '_' {
			b.WriteByte(rest[i])
			continue
		}
		if i+1 == len(rest) {
			return "", false
		}
		i++
		switch rest[i] {
		case '_':
			b.WriteByte('#')
		case 'd':
			b.WriteByte('.')
		case 'u':
			b.WriteByte('_')
		default:
			return "", false
		}
	}
	return b.String(), true
}

func (r *ComposeRunner) command() []string {
	if len(r.Command) > 0 {
		return r.Command
	}
	if _, err := exec.LookPath("podman"); err == nil {
		return []string{"podman", "compose"}
	}
	return []string{"docker", "compose"}
}

func (r *ComposeRunner) run(ctx context.Context, argv, env []string) error {
	if r.Run != nil {
		return r.Run(ctx, argv, env)
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = env
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w\n%s", strings.Join(argv, " "), err, lastLines(out.String(), 20))
	}
	return nil
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func (r *ComposeRunner) Start(ctx context.Context, inst Instance, app *App) (Instance, error) {
	if _, err := os.Stat(app.Runner.File); err != nil {
		return inst, sdk.Errorf(sdk.CodeInvalidArgs, "%s: compose file: %v", app.ID, err)
	}
	project := projectName(inst.ID)
	env := os.Environ()
	if r.Socket != "" {
		env = append(env, "DOCKER_HOST=unix://"+r.Socket)
	}
	vars := map[string]string{}
	for _, m := range []map[string]string{app.Env, inst.Env} { // for ${VAR} in the file
		for k, v := range m {
			vars[k] = v
		}
	}
	keys := make([]string, 0, len(vars))
	for k := range vars {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		env = append(env, k+"="+vars[k])
	}
	argv := append(append([]string(nil), r.command()...), "-p", project, "-f", app.Runner.File, "up", "-d", "--remove-orphans")
	if err := r.run(ctx, argv, env); err != nil {
		_ = r.Stop(ctx, inst) // what came up of it
		return inst, err
	}
	inst.Container = project
	return inst, nil
}

// containers are a project's containers.
func (r *ComposeRunner) containers(ctx context.Context, project string) ([]ContainerInfo, error) {
	list, err := r.Docker.List(ctx, labelProject)
	if err != nil {
		return nil, err
	}
	var out []ContainerInfo
	for _, c := range list {
		if c.Labels[labelProject] == project {
			out = append(out, c)
		}
	}
	return out, nil
}

// Stop stops and removes the project's containers; named volumes stay.
func (r *ComposeRunner) Stop(ctx context.Context, inst Instance) error {
	list, err := r.containers(ctx, projectName(inst.ID))
	if err != nil {
		return err
	}
	var firstErr error
	for _, c := range list {
		if c.Running || c.Restarting {
			if err := r.Docker.Stop(ctx, c.ID, 10*time.Second); err != nil && firstErr == nil {
				firstErr = err
			}
		}
		_ = r.Docker.Remove(ctx, c.ID)
	}
	return firstErr
}

func (r *ComposeRunner) Adopt(ctx context.Context) ([]Instance, error) {
	list, err := r.Docker.List(ctx, labelProject)
	if err != nil {
		return nil, err
	}
	byProject := map[string][]ContainerInfo{}
	for _, c := range list {
		if _, ok := instanceOfProject(c.Labels[labelProject]); ok {
			byProject[c.Labels[labelProject]] = append(byProject[c.Labels[labelProject]], c)
		}
	}
	projects := make([]string, 0, len(byProject))
	for p := range byProject {
		projects = append(projects, p)
	}
	sort.Strings(projects)
	var out []Instance
	for _, p := range projects {
		id, _ := instanceOfProject(p)
		inst := Instance{ID: id, App: appOf(id), Runner: RunnerCompose, Container: p, State: StateExited}
		for _, c := range byProject[p] {
			if c.Running || c.Restarting {
				inst.State = StateRunning
			}
		}
		if inst.State != StateRunning {
			for _, c := range byProject[p] {
				_ = r.Docker.Remove(ctx, c.ID)
			}
		}
		out = append(out, inst)
	}
	return out, nil
}

// Watch reports a project as ended once none of its containers runs (or
// is being restarted by the engine).
func (r *ComposeRunner) Watch(ctx context.Context, fn func(Ended)) error {
	return r.Docker.Events(ctx, labelProject, func(id string) {
		info, ok, err := r.Docker.Inspect(ctx, id)
		if err != nil || !ok {
			return // removed by hostd
		}
		project := info.Labels[labelProject]
		inst, mine := instanceOfProject(project)
		if !mine {
			return
		}
		list, err := r.containers(ctx, project)
		if err != nil {
			return
		}
		for _, c := range list {
			if c.Running || c.Restarting {
				return
			}
		}
		for _, c := range list {
			_ = r.Docker.Remove(ctx, c.ID)
		}
		fn(Ended{Instance: inst, ExitCode: info.ExitCode,
			Reason: fmt.Sprintf("the project's containers stopped (last exit status %d)", info.ExitCode)})
	})
}
