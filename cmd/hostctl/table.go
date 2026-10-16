package main

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/davitizhgenti/hostd/sdk"
)

// A column is one column of a table: its heading, and how a row's cell is
// written. Every table hostctl prints goes through writeTable, whether its
// columns are written here (hostctl ps) or come from a module's manifest
// (hostctl get).
type column[T any] struct {
	title string
	cell  func(T) string
}

// tableStyle: gap is the space between columns (default 2); a table with
// noHeader has no heading row (the log).
type tableStyle struct {
	gap      int
	noHeader bool
}

// writeTable writes rows under their headings, in aligned columns.
func writeTable[T any](w io.Writer, style tableStyle, cols []column[T], rows []T) error {
	gap := style.gap
	if gap == 0 {
		gap = 2
	}
	tw := tabwriter.NewWriter(w, 0, 0, gap, ' ', 0)
	cells := make([]string, len(cols))
	if !style.noHeader {
		for i, c := range cols {
			cells[i] = c.title
		}
		fmt.Fprintln(tw, strings.Join(cells, "\t"))
	}
	for _, r := range rows {
		for i, c := range cols {
			cells[i] = c.cell(r)
		}
		fmt.Fprintln(tw, strings.Join(cells, "\t"))
	}
	return tw.Flush()
}

// fetch reads a GET path into v. With --json it prints v as well, and done
// says the command has nothing left to do.
func (a *app) fetch(cmd *cobra.Command, path string, v any) (done bool, err error) {
	c, err := a.client()
	if err != nil {
		return false, err
	}
	if err := c.Get(cmd.Context(), path, v); err != nil {
		return false, err
	}
	if a.jsonOut {
		return true, a.printJSON(v)
	}
	return false, nil
}

// heldBy is the line for an action that was skipped because a change of
// higher priority holds its resource (and until when), or "" if it was not
// skipped.
func heldBy(res sdk.Result) string {
	if res.Status != sdk.StatusSkipped {
		return ""
	}
	line := "skipped: held by " + string(res.HeldBy)
	if res.Until != nil {
		line += " until " + res.Until.In(displayZone).Format("15:04:05")
	}
	return line
}

// shorten cuts s to n runes, marking the cut with "…".
func shorten(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
