package apps

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/davitizhgenti/hostd/sdk"
)

// Container labels that mark hostd's containers.
const (
	labelInstance = "hostd.instance"
	labelApp      = "hostd.app"
)

// ContainerSpec is what the docker runner asks the engine to create.
type ContainerSpec struct {
	Image   string
	Env     []string
	Labels  map[string]string
	Ports   []string // "8080:80", "127.0.0.1:8080:80", "53:53/udp"
	Restart string   // no, always, on-failure
}

// ContainerInfo is what the docker runner reads back.
type ContainerInfo struct {
	ID         string
	Name       string
	Labels     map[string]string
	Running    bool
	Restarting bool
	Status     string // created, running, exited...
	ExitCode   int
	PID        int
}

// Docker is the part of the Docker Engine API the docker runner uses.
// Podman serves the same API on its socket.
type Docker interface {
	ImageExists(ctx context.Context, image string) (bool, error)
	Pull(ctx context.Context, image string) error
	Create(ctx context.Context, name string, spec ContainerSpec) (string, error)
	Start(ctx context.Context, id string) error
	Stop(ctx context.Context, id string, timeout time.Duration) error
	Remove(ctx context.Context, id string) error
	Inspect(ctx context.Context, id string) (ContainerInfo, bool, error)
	List(ctx context.Context, label string) ([]ContainerInfo, error)
	// Events calls fn with the ID of each labelled container that dies.
	Events(ctx context.Context, label string, fn func(id string)) error
}

// DockerRunner runs container apps, one container per instance, named
// hostd-<instance> and labelled with the instance and app.
type DockerRunner struct {
	Docker Docker
}

func containerName(instance string) string {
	return "hostd-" + strings.ReplaceAll(instance, "#", "_")
}

func restartPolicy(r string) string {
	switch r {
	case "always":
		return "always"
	case "on-failure":
		return "on-failure"
	default:
		return "no"
	}
}

func (r *DockerRunner) Start(ctx context.Context, inst Instance, app *App) (Instance, error) {
	image, ports := app.Runner.Image, app.Runner.Ports
	if inst.Image != "" { // a release
		image, ports = inst.Image, inst.Ports
	}
	if image == "" {
		return inst, sdk.Errorf(sdk.CodeModuleUnavailable,
			"app %q builds its image from %q; building arrives with deploys (M4), set image for now", app.ID, app.Runner.Build)
	}
	ok, err := r.Docker.ImageExists(ctx, image)
	if err != nil {
		return inst, err
	}
	if !ok {
		if err := r.Docker.Pull(ctx, image); err != nil {
			return inst, fmt.Errorf("pulling %s: %w", image, err)
		}
	}
	name := containerName(inst.ID)
	// A container left from an earlier run with this ID would block the
	// name.
	if old, ok, err := r.Docker.Inspect(ctx, name); err == nil && ok {
		if old.Running {
			return inst, sdk.Errorf(sdk.CodeInternal, "container %s is already running", name)
		}
		_ = r.Docker.Remove(ctx, old.ID)
	}
	vars := map[string]string{}
	for _, m := range []map[string]string{app.Env, inst.Env} { // a release's (PORT) last
		for k, v := range m {
			vars[k] = v
		}
	}
	env := make([]string, 0, len(vars))
	for k, v := range vars {
		env = append(env, k+"="+v)
	}
	sort.Strings(env)
	id, err := r.Docker.Create(ctx, name, ContainerSpec{
		Image: image, Env: env, Ports: ports, Restart: restartPolicy(app.Restart),
		Labels: map[string]string{labelInstance: inst.ID, labelApp: inst.App},
	})
	if err != nil {
		return inst, err
	}
	if err := r.Docker.Start(ctx, id); err != nil {
		_ = r.Docker.Remove(ctx, id)
		return inst, err
	}
	inst.Container = name
	if info, ok, err := r.Docker.Inspect(ctx, id); err == nil && ok {
		inst.PID = info.PID
	}
	return inst, nil
}

func (r *DockerRunner) Stop(ctx context.Context, inst Instance) error {
	name := containerName(inst.ID)
	info, ok, err := r.Docker.Inspect(ctx, name)
	if err != nil || !ok {
		return err
	}
	if info.Running || info.Restarting {
		if err := r.Docker.Stop(ctx, info.ID, 10*time.Second); err != nil {
			// Engines can fail while tearing down a stopped container's
			// network; what counts is whether it still runs.
			after, ok, ierr := r.Docker.Inspect(ctx, info.ID)
			if ierr != nil || (ok && (after.Running || after.Restarting)) {
				return err
			}
		}
	}
	// The container has stopped, which is what was asked. If removing it
	// fails, the next start or adopt removes the leftover.
	_ = r.Docker.Remove(ctx, info.ID)
	return nil
}

func (r *DockerRunner) Adopt(ctx context.Context) ([]Instance, error) {
	list, err := r.Docker.List(ctx, labelInstance)
	if err != nil {
		return nil, err
	}
	var out []Instance
	for _, c := range list {
		inst := Instance{ID: c.Labels[labelInstance], App: c.Labels[labelApp], Runner: RunnerDocker,
			Container: strings.TrimPrefix(c.Name, "/"), PID: c.PID, State: StateRunning}
		if !c.Running && !c.Restarting {
			code := c.ExitCode
			inst.ExitCode = &code
			inst.State = StateExited
			if code != 0 {
				inst.State = StateFailed
			}
			_ = r.Docker.Remove(ctx, c.ID)
		}
		out = append(out, inst)
	}
	return out, nil
}

// Watch reports containers that die and are not being restarted by the
// engine (restart = "always" or "on-failure" keeps the instance alive).
func (r *DockerRunner) Watch(ctx context.Context, fn func(Ended)) error {
	return r.Docker.Events(ctx, labelInstance, func(id string) {
		info, ok, err := r.Docker.Inspect(ctx, id)
		if err != nil || !ok || info.Running || info.Restarting {
			return // stopped and removed by hostd, or restarting
		}
		_ = r.Docker.Remove(ctx, id)
		fn(Ended{Instance: info.Labels[labelInstance], ExitCode: info.ExitCode,
			Reason: fmt.Sprintf("container exited with status %d", info.ExitCode)})
	})
}

// --- the Engine API over a unix socket -------------------------------------

// EngineAPI talks to Docker or Podman over its unix socket.
type EngineAPI struct {
	Socket string
	http   *http.Client
}

// DefaultEngineSocket finds the container engine socket: rootless Podman,
// then rootless Docker, then the system Docker socket.
func DefaultEngineSocket(runtimeDir string) string {
	for _, s := range []string{
		filepath.Join(runtimeDir, "podman", "podman.sock"),
		filepath.Join(runtimeDir, "docker.sock"),
		"/var/run/docker.sock",
	} {
		if fi, err := os.Stat(s); err == nil && fi.Mode()&os.ModeSocket != 0 {
			return s
		}
	}
	return filepath.Join(runtimeDir, "podman", "podman.sock")
}

func (e *EngineAPI) client() *http.Client {
	if e.http == nil {
		e.http = &http.Client{Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", e.Socket)
			},
		}}
	}
	return e.http
}

const apiBase = "http://engine/v1.41"

// do sends a request; status 404 is reported as errNotFound.
func (e *EngineAPI) do(ctx context.Context, method, path string, body any, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, apiBase+path, rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := e.client().Do(req)
	if err != nil {
		return sdk.Errorf(sdk.CodeModuleUnavailable, "container engine not reachable at %s: %v", e.Socket, err)
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(res.Body, 16<<20))
	switch {
	case res.StatusCode == http.StatusNotFound:
		return errNotFound
	case res.StatusCode == http.StatusNotModified: // already started or stopped
		return nil
	case res.StatusCode >= 400:
		var m struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(data, &m)
		if m.Message == "" {
			m.Message = strings.TrimSpace(string(data))
		}
		return fmt.Errorf("container engine: %s", m.Message)
	}
	if out != nil && len(data) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}

var errNotFound = errors.New("not found")

func (e *EngineAPI) ImageExists(ctx context.Context, image string) (bool, error) {
	err := e.do(ctx, http.MethodGet, "/images/"+url.PathEscape(image)+"/json", nil, nil)
	if errors.Is(err, errNotFound) {
		return false, nil
	}
	return err == nil, err
}

// Pull downloads an image. The engine streams progress; the last message
// carries any error.
func (e *EngineAPI) Pull(ctx context.Context, image string) error {
	q := url.Values{"fromImage": {image}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiBase+"/images/create?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	res, err := e.client().Do(req)
	if err != nil {
		return sdk.Errorf(sdk.CodeModuleUnavailable, "container engine not reachable at %s: %v", e.Socket, err)
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		return fmt.Errorf("pull %s: %s", image, strings.TrimSpace(string(b)))
	}
	dec := json.NewDecoder(res.Body)
	for {
		var msg struct {
			Error string `json:"error"`
		}
		if err := dec.Decode(&msg); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if msg.Error != "" {
			return fmt.Errorf("pull %s: %s", image, msg.Error)
		}
	}
}

func (e *EngineAPI) Create(ctx context.Context, name string, spec ContainerSpec) (string, error) {
	exposed := map[string]struct{}{}
	bindings := map[string][]map[string]string{}
	for _, p := range spec.Ports {
		hostIP, hostPort, ctrPort, err := parsePort(p)
		if err != nil {
			return "", sdk.Errorf(sdk.CodeInvalidArgs, "%v", err)
		}
		exposed[ctrPort] = struct{}{}
		bindings[ctrPort] = append(bindings[ctrPort], map[string]string{"HostIp": hostIP, "HostPort": hostPort})
	}
	body := map[string]any{
		"Image": spec.Image, "Env": spec.Env, "Labels": spec.Labels, "ExposedPorts": exposed,
		"HostConfig": map[string]any{
			"PortBindings":  bindings,
			"RestartPolicy": map[string]any{"Name": spec.Restart},
		},
	}
	var out struct {
		ID string `json:"Id"`
	}
	err := e.do(ctx, http.MethodPost, "/containers/create?name="+url.QueryEscape(name), body, &out)
	return out.ID, err
}

// parsePort reads "[ip:]host:container[/proto]".
func parsePort(p string) (hostIP, hostPort, ctrPort string, err error) {
	proto := "tcp"
	if base, pr, ok := strings.Cut(p, "/"); ok {
		p, proto = base, pr
	}
	parts := strings.Split(p, ":")
	switch len(parts) {
	case 2:
		hostPort, ctrPort = parts[0], parts[1]
	case 3:
		hostIP, hostPort, ctrPort = parts[0], parts[1], parts[2]
	default:
		return "", "", "", fmt.Errorf("port %q: use host:container, e.g. 8080:80", p)
	}
	for _, n := range []string{hostPort, ctrPort} {
		if v, err := strconv.Atoi(n); err != nil || v < 1 || v > 65535 {
			return "", "", "", fmt.Errorf("port %q: %q is not a port number", p, n)
		}
	}
	return hostIP, hostPort, ctrPort + "/" + proto, nil
}

func (e *EngineAPI) Start(ctx context.Context, id string) error {
	return e.do(ctx, http.MethodPost, "/containers/"+url.PathEscape(id)+"/start", nil, nil)
}

func (e *EngineAPI) Stop(ctx context.Context, id string, timeout time.Duration) error {
	q := "?t=" + strconv.Itoa(int(timeout.Seconds()))
	err := e.do(ctx, http.MethodPost, "/containers/"+url.PathEscape(id)+"/stop"+q, nil, nil)
	if errors.Is(err, errNotFound) {
		return nil
	}
	return err
}

func (e *EngineAPI) Remove(ctx context.Context, id string) error {
	err := e.do(ctx, http.MethodDelete, "/containers/"+url.PathEscape(id)+"?force=true", nil, nil)
	if errors.Is(err, errNotFound) {
		return nil
	}
	return err
}

type inspectJSON struct {
	ID     string `json:"Id"`
	Name   string `json:"Name"`
	Config struct {
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
	State struct {
		Status     string `json:"Status"`
		Running    bool   `json:"Running"`
		Restarting bool   `json:"Restarting"`
		ExitCode   int    `json:"ExitCode"`
		Pid        int    `json:"Pid"`
	} `json:"State"`
}

func (j inspectJSON) info() ContainerInfo {
	return ContainerInfo{ID: j.ID, Name: strings.TrimPrefix(j.Name, "/"), Labels: j.Config.Labels,
		Running: j.State.Running, Restarting: j.State.Restarting, Status: j.State.Status,
		ExitCode: j.State.ExitCode, PID: j.State.Pid}
}

func (e *EngineAPI) Inspect(ctx context.Context, id string) (ContainerInfo, bool, error) {
	var j inspectJSON
	err := e.do(ctx, http.MethodGet, "/containers/"+url.PathEscape(id)+"/json", nil, &j)
	if errors.Is(err, errNotFound) {
		return ContainerInfo{}, false, nil
	}
	if err != nil {
		return ContainerInfo{}, false, err
	}
	return j.info(), true, nil
}

func (e *EngineAPI) List(ctx context.Context, label string) ([]ContainerInfo, error) {
	filters, _ := json.Marshal(map[string][]string{"label": {label}})
	var list []struct {
		ID string `json:"Id"`
	}
	if err := e.do(ctx, http.MethodGet, "/containers/json?all=true&filters="+url.QueryEscape(string(filters)), nil, &list); err != nil {
		return nil, err
	}
	out := make([]ContainerInfo, 0, len(list))
	for _, c := range list {
		if info, ok, err := e.Inspect(ctx, c.ID); err == nil && ok {
			out = append(out, info)
		}
	}
	return out, nil
}

// Events follows the engine's event stream until ctx ends or the stream
// breaks; the caller reconnects.
func (e *EngineAPI) Events(ctx context.Context, label string, fn func(id string)) error {
	filters, _ := json.Marshal(map[string][]string{"label": {label}, "type": {"container"}, "event": {"die"}})
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiBase+"/events?filters="+url.QueryEscape(string(filters)), nil)
	if err != nil {
		return err
	}
	res, err := e.client().Do(req)
	if err != nil {
		return sdk.Errorf(sdk.CodeModuleUnavailable, "container engine not reachable at %s: %v", e.Socket, err)
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		return fmt.Errorf("container events: HTTP %d", res.StatusCode)
	}
	sc := bufio.NewScanner(res.Body)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		var ev struct {
			Action string `json:"Action"`
			Status string `json:"status"`
			Actor  struct {
				ID string `json:"ID"`
			} `json:"Actor"`
			ID string `json:"id"`
		}
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			continue
		}
		action := ev.Action
		if action == "" {
			action = ev.Status
		}
		id := ev.Actor.ID
		if id == "" {
			id = ev.ID
		}
		if action == "die" && id != "" {
			fn(id)
		}
	}
	if ctx.Err() != nil {
		return nil
	}
	return sc.Err()
}
