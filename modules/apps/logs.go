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

// journal reads a user unit's last lines from the journal.
func journal(ctx context.Context, unit string, n int) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "journalctl", "--user", "-u", unit, "-n", strconv.Itoa(n), "--no-pager", "-o", "short-iso")
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
// 5000), running or recently ended. A handed-off app (a Steam game) logs
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
	lines, err := read(ctx, unit, n)
	if err != nil {
		return nil, err
	}
	return Logs{Instance: in.ID, Unit: unit, Lines: lines}, nil
}
