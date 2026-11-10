package deploy

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Users beside the first. The machine's first hostd user (screen) owns the
// screen; others are users like any: each has a hostd of its own, as that
// user's systemd service, with its own port, its own tokens and its own
// certificate authority. /etc/hostd/users lists them ("name port"), and
// every update brings each of their hostds up to date with the first.

const usersFile = "/etc/hostd/users"

// firstExtraPort is where hostd looks for a free port for another user:
// the first user has 7300.
const firstExtraPort = 7301

var reUserName = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

// hostdUser is a user with a hostd of its own.
type hostdUser struct {
	Name string
	Port int
}

// users reads the list.
func (s *Setup) users() []hostdUser {
	f, err := os.Open(s.path(usersFile))
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []hostdUser
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		port := 0
		if len(fields) > 1 {
			port, _ = strconv.Atoi(fields[1])
		}
		out = append(out, hostdUser{Name: fields[0], Port: port})
	}
	return out
}

func (s *Setup) saveUsers(list []hostdUser) error {
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	var b strings.Builder
	b.WriteString("# Users with a hostd of their own, beside the first: name and API port.\n# Managed by hostd-setup user add / remove.\n")
	for _, u := range list {
		fmt.Fprintf(&b, "%s %d\n", u.Name, u.Port)
	}
	if err := os.MkdirAll(filepath.Dir(s.path(usersFile)), 0o755); err != nil {
		return err
	}
	return os.WriteFile(s.path(usersFile), []byte(b.String()), 0o644)
}

// freePort returns the lowest port from firstExtraPort that no listed user has.
func freePort(list []hostdUser) int {
	used := map[int]bool{}
	for _, u := range list {
		used[u.Port] = true
	}
	port := firstExtraPort
	for used[port] {
		port++
	}
	return port
}

// ListUsers prints the users with a hostd of their own.
func (s *Setup) ListUsers(w io.Writer) error {
	list := s.users()
	if len(list) == 0 {
		_, err := fmt.Fprintln(w, "no users besides the first")
		return err
	}
	for _, u := range list {
		if _, err := fmt.Fprintf(w, "%s\t:%d\n", u.Name, u.Port); err != nil {
			return err
		}
	}
	return nil
}

// AddUser gives a user a hostd of their own: the user is made if they do not
// exist, hostd is installed for them and started on their own port. port 0
// takes the next free one. The first user (o.ScreenUser) keeps the screen
// and the machine's setup; this only does what a user needs.
func (s *Setup) AddUser(ctx context.Context, o Options, name string, port int) error {
	port, list, err := s.planUser(o, name, port)
	if err != nil {
		return err
	}
	o.ScreenUser = name
	if err := s.runExtra(ctx, o, port); err != nil {
		return err
	}
	if err := s.listUser(list, name, port); err != nil {
		return err
	}
	s.Out.Info("hostd for %s is on port %d: hostctl login <this machine>:%d", name, port, port)
	return nil
}

// AdminAddUser is hostd-admin's user add: it makes the account and what a
// user's hostd needs from root (groups, subordinate IDs, lingering) and
// gives the user a port, and nothing more. The user installs hostd
// themselves, with no root: hostd-setup reads the port from the list.
func (s *Setup) AdminAddUser(ctx context.Context, o Options, name string, port int) error {
	port, list, err := s.planUser(o, name, port)
	if err != nil {
		return err
	}
	o.ScreenUser = name
	if o.GPU == "" {
		o.GPU = "auto"
	}
	in := &installer{Setup: s, o: o, extra: true, port: port}
	for _, step := range []func(context.Context) error{in.checkSystem, in.screenUser} {
		if err := step(ctx); err != nil {
			return err
		}
	}
	if err := s.listUser(list, name, port); err != nil {
		return err
	}
	s.Out.Info("%s may have a hostd on port %d. As %s (no root): ./deploy/install.sh   (hostd-setup)", name, port, name)
	return nil
}

// planUser checks a new user's name and port against the list and picks the
// port (0: the next free one, or the one the user has already).
func (s *Setup) planUser(o Options, name string, port int) (_ int, list []hostdUser, _ error) {
	first := o.ScreenUser
	if first == "" {
		first = "screen"
	}
	list = s.users()
	switch {
	case !reUserName.MatchString(name):
		return 0, nil, fmt.Errorf("%q cannot be a user name: lowercase letters, digits, '_' and '-'", name)
	case name == first:
		return 0, nil, fmt.Errorf("%s is the machine's first user: it has hostd already", name)
	case port != 0 && (port < 1024 || port > 65535):
		return 0, nil, fmt.Errorf("port %d: use 1024-65535", port)
	}
	for _, u := range list {
		if u.Port == port && u.Name != name {
			return 0, nil, fmt.Errorf("port %d is hostd's for %s", port, u.Name)
		}
	}
	if port == 0 {
		port = freePort(list)
		for _, u := range list {
			if u.Name == name {
				port = u.Port // adding again keeps the port
			}
		}
	}
	if port == 7300 {
		return 0, nil, fmt.Errorf("port 7300 is the first user's")
	}
	return port, list, nil
}

// listUser records name's port in the list.
func (s *Setup) listUser(list []hostdUser, name string, port int) error {
	kept := list[:0:0]
	for _, u := range list {
		if u.Name != name {
			kept = append(kept, u)
		}
	}
	return s.saveUsers(append(kept, hostdUser{Name: name, Port: port}))
}

// runExtra does what one more user needs, for the user in o.ScreenUser.
func (s *Setup) runExtra(ctx context.Context, o Options, port int) error {
	if o.GPU == "" {
		o.GPU = "auto"
	}
	in := &installer{Setup: s, o: o, extra: true, port: port}
	work, err := os.MkdirTemp("", "hostd-setup-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	in.work = work
	for _, step := range []func(context.Context) error{in.checkSystem, in.screenUser, in.userConfig, in.installHostd} {
		if err := step(ctx); err != nil {
			return err
		}
	}
	return nil
}

// updateUsers brings every listed user's hostd up to date, as part of an
// install or an update of the first. A user whose update fails is reported
// and does not stop the others.
func (s *Setup) updateUsers(ctx context.Context, o Options) error {
	first := o.ScreenUser
	var failed []string
	for _, u := range s.users() {
		if u.Name == first {
			continue
		}
		o2 := o
		o2.ScreenUser = u.Name
		s.Out.Step("hostd for " + u.Name)
		if err := s.runExtra(ctx, o2, u.Port); err != nil {
			s.Out.Warn("warning: %s: %v", u.Name, err)
			failed = append(failed, u.Name)
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("could not update hostd for %s", strings.Join(failed, ", "))
	}
	return nil
}

// RemoveUser stops a user's hostd and takes them off the list. With purge
// their hostd data (config, tokens, releases) goes too. The user and their
// files stay: they are the machine's, not hostd's.
func (s *Setup) RemoveUser(ctx context.Context, name string, purge bool) error {
	list := s.users()
	found := false
	kept := list[:0:0]
	for _, u := range list {
		if u.Name == name {
			found = true
			continue
		}
		kept = append(kept, u)
	}
	if !found {
		return fmt.Errorf("%s has no hostd of their own (hostd-setup user list)", name)
	}
	if _, err := s.run(ctx, "systemctl", "--user", "--machine="+name+"@", "disable", "--now", "hostd.service"); err != nil {
		s.Out.Warn("warning: could not stop hostd for %s: %v", name, err)
	}
	if err := s.saveUsers(kept); err != nil {
		return err
	}
	s.Out.Info("hostd for %s is stopped and unlisted", name)
	if purge {
		home, err := s.homeOf(ctx, name)
		if err != nil {
			return err
		}
		for _, p := range []string{"/.config/hostd", "/.config/hostctl", "/.local/state/hostd", "/.local/lib/hostd", "/.config/systemd/user/hostd.service"} {
			if err := os.RemoveAll(s.path(home + p)); err != nil {
				return err
			}
		}
		s.Out.Info("deleted %s's hostd data", name)
	}
	return nil
}

// userConfig gives another user's hostd its port, in a config file of its
// own that is written once: a person's changes to it stay.
func (in *installer) userConfig(context.Context) error {
	conf := in.user.home + "/.config/hostd/hostd.toml"
	if in.exists(conf) {
		in.Out.Info("hostd config exists")
		return nil
	}
	if err := in.mkdirs(filepath.Dir(conf), in.user.name); err != nil {
		return err
	}
	body := fmt.Sprintf("# hostd for %s. Its API is on its own port: the first user's hostd has 7300.\nlisten = \":%d\"\n", in.user.name, in.port)
	if err := os.WriteFile(in.path(conf), []byte(body), 0o644); err != nil {
		return err
	}
	if err := in.chown(in.path(conf), in.user.name); err != nil {
		return err
	}
	in.Out.Changed("hostd for %s listens on port %d", in.user.name, in.port)
	return nil
}

// userPort is user mode's step for a user the admin gave a port (hostd-admin
// user add): their hostd listens there, not on the first user's 7300.
func (in *installer) userPort(ctx context.Context) error {
	for _, u := range in.users() {
		if u.Name == in.user.name && u.Port > 0 {
			in.port = u.Port
			return in.userConfig(ctx)
		}
	}
	return nil
}
