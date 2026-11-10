package deploy

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// Seats: the users that may hold the screen, each with a graphical session
// on a VT of its own. /etc/hostd/seats lists "USER VT"; the seat helper (the
// seats add-on) reads it, and for each user but the one greetd logs in, a
// unit hostd-session-USER.service starts their Sway on their VT.

const seatsFile = "/etc/hostd/seats"

type seatUser struct {
	User string
	VT   int
}

func (s *Setup) seats() []seatUser {
	f, err := os.Open(s.path(seatsFile))
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []seatUser
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(strings.SplitN(sc.Text(), "#", 2)[0])
		if len(fields) >= 2 {
			if vt, err := strconv.Atoi(fields[1]); err == nil {
				out = append(out, seatUser{fields[0], vt})
			}
		}
	}
	return out
}

func (s *Setup) saveSeats(list []seatUser) error {
	sort.Slice(list, func(i, j int) bool { return list[i].VT < list[j].VT })
	var b strings.Builder
	b.WriteString("# Users that may hold the seat, and the VT of their session: USER VT.\n# Managed by hostd-admin seats add / remove.\n")
	for _, u := range list {
		fmt.Fprintf(&b, "%s %d\n", u.User, u.VT)
	}
	if err := s.mkdirs("/etc/hostd", "root"); err != nil {
		return err
	}
	return os.WriteFile(s.path(seatsFile), []byte(b.String()), 0o644)
}

// greetdUser is the user greetd logs in (its session is on VT 7 already).
func (s *Setup) greetdUser() string {
	b, _ := os.ReadFile(s.path("/etc/greetd/config.toml"))
	inInitial := false
	for _, l := range strings.Split(string(b), "\n") {
		l = strings.TrimSpace(l)
		switch {
		case strings.HasPrefix(l, "["):
			inInitial = l == "[initial_session]"
		case inInitial && strings.HasPrefix(l, "user"):
			_, v, _ := strings.Cut(l, "=")
			return strings.Trim(strings.TrimSpace(v), `"`)
		}
	}
	return ""
}

func sessionUnit(user string) string { return "hostd-session-" + user + ".service" }

// AddSeat lets user hold the seat, on this VT.
func (s *Setup) AddSeat(ctx context.Context, user string, vt int) error {
	if !reUserName.MatchString(user) {
		return fmt.Errorf("%q cannot be a user name", user)
	}
	if vt < 1 || vt > 63 {
		return fmt.Errorf("VT %d: use 1-63", vt)
	}
	if !s.ok(ctx, "id", user) {
		return fmt.Errorf("no user %s", user)
	}
	list := s.seats()
	for _, u := range list {
		if u.VT == vt && u.User != user {
			return fmt.Errorf("VT %d is %s's session", vt, u.User)
		}
	}
	if !slices.Contains(s.adminInstalled(), "seats") {
		s.Out.Warn("warning: the seats add-on is not installed: sudo hostd-admin add seats")
	}
	list = slices.DeleteFunc(list, func(u seatUser) bool { return u.User == user })
	if err := s.saveSeats(append(list, seatUser{user, vt})); err != nil {
		return err
	}
	s.Out.Changed("%s may hold the seat, on VT %d", user, vt)
	if user == s.greetdUser() {
		s.Out.Info("%s's session is greetd's: no unit needed", user)
		return nil
	}
	sway := "/usr/bin/sway"
	if _, err := os.Stat(s.path("/usr/local/lib/hostd/start-sway")); err == nil {
		sway = "/usr/local/lib/hostd/start-sway"
	}
	tmpl, err := fs.ReadFile(embedded, "addons/seats/hostd-session.service")
	if err != nil {
		return err
	}
	text := strings.NewReplacer("@USER@", user, "@VT@", strconv.Itoa(vt), "@SWAY@", sway).Replace(string(tmpl))
	unit := sessionUnit(user)
	changed, err := s.installFile([]byte(text), "/etc/systemd/system/"+unit, 0o644, "root")
	if err != nil {
		return err
	}
	if changed {
		_, _ = s.run(ctx, "systemctl", "daemon-reload")
	}
	if _, err := s.run(ctx, "systemctl", "enable", "--now", unit); err != nil {
		s.Out.Warn("warning: could not start %s (journalctl -u %s)", unit, unit)
	} else {
		s.Out.Changed("%s's session runs on VT %d", user, vt)
	}
	return nil
}

// RemoveSeat takes user off the list and stops their session.
func (s *Setup) RemoveSeat(ctx context.Context, user string) error {
	list := s.seats()
	if !slices.ContainsFunc(list, func(u seatUser) bool { return u.User == user }) {
		return fmt.Errorf("%s is not a seat user (hostd-admin seats list)", user)
	}
	unit := sessionUnit(user)
	if _, err := os.Stat(s.path("/etc/systemd/system/" + unit)); err == nil {
		_, _ = s.run(ctx, "systemctl", "disable", "--now", unit)
		_ = os.Remove(s.path("/etc/systemd/system/" + unit))
		_, _ = s.run(ctx, "systemctl", "daemon-reload")
	}
	list = slices.DeleteFunc(list, func(u seatUser) bool { return u.User == user })
	if err := s.saveSeats(list); err != nil {
		return err
	}
	s.Out.Changed("%s no longer holds the seat", user)
	return nil
}

// ListSeats prints the seat users.
func (s *Setup) ListSeats(w io.Writer) error {
	list := s.seats()
	if len(list) == 0 {
		fmt.Fprintln(w, "no seat users: sudo hostd-admin seats add USER VT")
		return nil
	}
	sort.Slice(list, func(i, j int) bool { return list[i].VT < list[j].VT })
	for _, u := range list {
		fmt.Fprintf(w, "%-16s VT %d\n", u.User, u.VT)
	}
	return nil
}
