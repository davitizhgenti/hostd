package main

import (
	"bufio"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

// A hostSystem is one user's hostd on this machine: the first user's on port
// 7300, the others' on the ports /etc/hostd/users lists.
type hostSystem struct {
	Name    string
	Port    int
	Running bool
}

// machineSystems finds them: every user that has a hostd socket in /run/user,
// and every user the admin listed.
func machineSystems(runDir, usersFile string) []hostSystem {
	ports := map[string]int{}
	if f, err := os.Open(usersFile); err == nil {
		defer f.Close()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			fields := strings.Fields(sc.Text())
			if len(fields) == 0 || strings.HasPrefix(fields[0], "#") {
				continue
			}
			port := 0
			if len(fields) > 1 {
				port, _ = strconv.Atoi(fields[1])
			}
			ports[fields[0]] = port
		}
	}
	running := map[string]bool{}
	socks, _ := filepath.Glob(filepath.Join(runDir, "*", "hostd.sock"))
	for _, s := range socks {
		if u, err := user.LookupId(filepath.Base(filepath.Dir(s))); err == nil {
			running[u.Username] = true
		}
	}
	names := map[string]bool{}
	for n := range ports {
		names[n] = true
	}
	for n := range running {
		names[n] = true
	}
	var out []hostSystem
	for n := range names {
		port, listed := ports[n]
		if !listed {
			port = 7300 // not in the list: the first user's
		}
		out = append(out, hostSystem{n, port, running[n]})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Port < out[j].Port || (out[i].Port == out[j].Port && out[i].Name < out[j].Name)
	})
	return out
}

// hostctl systems: the hostds on this machine.
func (a *app) systemsCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "systems",
		Short: "List the hostds on this machine (one per user)",
		Long: `Every user can have a hostd of their own, with its own port, tokens and apps.
This lists them with their API address, and whether each runs. Save a login to one
with: hostctl login https://<machine>:PORT --name NAME`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			rows := machineSystems("/run/user", "/etc/hostd/users")
			if len(rows) == 0 {
				fmt.Fprintln(a.stdout, "no hostd on this machine")
				return nil
			}
			return writeTable(a.stdout, tableStyle{}, []column[hostSystem]{
				{"USER", func(r hostSystem) string { return r.Name }},
				{"PORT", func(r hostSystem) string { return strconv.Itoa(r.Port) }},
				{"RUNNING", func(r hostSystem) string {
					if r.Running {
						return "yes"
					}
					return "no"
				}},
			}, rows)
		},
	}
}
