package apps

import (
	"bytes"
	"context"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/davitizhgenti/hostd/contract"
	"github.com/davitizhgenti/hostd/sdk"
)

// Logs is an instance's latest log lines.
type Logs struct {
	Instance string   `json:"instance"`
	Unit     string   `json:"unit"`
	Lines    []string `json:"lines"`
}

// JournalQuery is what an instance's logs are read with.
type JournalQuery struct {
	Unit  string
	Lines int       // the last ones
	Since time.Time // the instance's start: an earlier instance of the same name logged in the same unit
	Plain bool      // the messages only, without time and process
}

// journal reads a user unit's last lines from the journal.
func journal(ctx context.Context, q JournalQuery) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var stderr bytes.Buffer
	args := []string{"--user", "-n", strconv.Itoa(q.Lines), "--no-pager"}
	if q.Plain {
		// The messages of the app's own processes, not systemd's about
		// starting and stopping its unit.
		args = append(args, "-o", "cat", "_SYSTEMD_USER_UNIT="+q.Unit)
	} else {
		args = append(args, "-o", "short-iso", "-u", q.Unit)
	}
	if !q.Since.IsZero() {
		// To the microsecond: an earlier instance with the same name, a moment
		// before, logged in the same unit.
		args = append(args, "--since", q.Since.UTC().Format("2006-01-02 15:04:05.000000")+" UTC")
	}
	cmd := exec.CommandContext(ctx, "journalctl", args...)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, sdk.Errorf(sdk.CodeModuleUnavailable, "journalctl: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if len(lines) == 1 && (lines[0] == "" || lines[0] == "-- No entries --") {
		lines = []string{}
	}
	return lines, nil
}

// readLogs returns an instance's last lines (?lines=, default 200, at most
// 5000; ?plain=true for the messages only), running or recently ended,
// from its start on. A handed-off app (a Steam game) logs
// in its launcher's unit, which is what this reads.
func (m *Module) readLogs(ctx context.Context, params map[string]string) (any, error) {
	in, err := m.readInstance(params["id"])
	if err != nil {
		return nil, err
	}
	if in.Runner == RunnerDocker || in.Runner == RunnerCompose {
		return nil, sdk.Errorf(sdk.CodeNotFound, "%s runs in a container: see its container's logs", in.ID)
	}
	n := 200
	if v, err := strconv.Atoi(params["lines"]); err == nil && v > 0 {
		n = min(v, 5000)
	}
	unit := in.Unit
	if unit == "" {
		unit = contract.UnitName(in.ID)
	}
	read := m.opts.Journal
	if read == nil {
		read = journal
	}
	plain := params["plain"] == "true" || params["plain"] == "1"
	lines, err := read(ctx, JournalQuery{Unit: unit, Lines: n, Since: in.Started, Plain: plain})
	if err != nil {
		return nil, err
	}
	return Logs{Instance: in.ID, Unit: unit, Lines: lines}, nil
}
