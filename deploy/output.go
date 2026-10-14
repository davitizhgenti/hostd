package deploy

import (
	"fmt"
	"io"
	"strings"
	"sync"
)

// Output prints the installer's steps: a heading per step, and indented
// lines under it. In quiet mode (an update) a step is shown only if it
// changed something, warned, or printed a command's output; the others are
// counted, and Done says how many.
type Output struct {
	w       io.Writer
	verbose bool
	// Markers start change and warning lines with \001, for the awk filter
	// of the shell installer of earlier versions, when it is the one that
	// runs this one (hostd-setup update on a machine not yet moved over).
	Markers bool

	mu          sync.Mutex
	title       string   // the current step
	pending     []string // its lines, while it is not shown
	shown       bool     // its heading is out
	always      bool     // shown even if nothing changed (Done)
	hidden      int      // steps that had nothing to say
	changed     bool     // anything changed, in any step
	stepChanged bool     // something changed in this step
}

// NewOutput prints to w; verbose shows every step.
func NewOutput(w io.Writer, verbose bool) *Output { return &Output{w: w, verbose: verbose} }

// Step starts a step.
func (o *Output) Step(title string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.finish()
	o.title, o.pending, o.shown, o.always, o.stepChanged = title, nil, false, false, false
	if o.verbose {
		o.show()
	}
}

// Info is a line about something that needed no change.
func (o *Output) Info(format string, a ...any) { o.line(false, format, a...) }

// Changed is a line about a change on the machine.
func (o *Output) Changed(format string, a ...any) {
	o.mu.Lock()
	o.changed, o.stepChanged = true, true
	o.mu.Unlock()
	o.mark()
	o.line(true, format, a...)
}

// Warn is a line that needs attention: always shown.
func (o *Output) Warn(format string, a ...any) {
	o.mark()
	o.line(true, format, a...)
}

func (o *Output) mark() {
	if o.Markers {
		o.mu.Lock()
		o.show()
		_, _ = io.WriteString(o.w, "\001")
		o.mu.Unlock()
	}
}

// StepChanged reports whether the current step changed something.
func (o *Output) StepChanged() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.stepChanged
}

// AnyChanged reports whether any step changed something.
func (o *Output) AnyChanged() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.changed
}

// Writer is where a command's own output goes: it shows the step, since
// whatever a command prints is worth seeing.
func (o *Output) Writer() io.Writer { return rawWriter{o} }

type rawWriter struct{ o *Output }

func (r rawWriter) Write(p []byte) (int, error) {
	r.o.mu.Lock()
	defer r.o.mu.Unlock()
	r.o.show()
	return r.o.w.Write(p)
}

// Done starts the last step, which is always shown, and is where the
// summary goes; Close ends it.
func (o *Output) Done() {
	o.Step("Done")
	o.mu.Lock()
	o.always = true
	o.show()
	o.mu.Unlock()
}

// Close ends the output: the count of steps that had nothing to say.
func (o *Output) Close() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.finish()
	if o.hidden > 0 && !o.verbose {
		fmt.Fprintf(o.w, "\n(%d steps already up to date; hostd-setup update --verbose lists them)\n", o.hidden)
	}
}

func (o *Output) line(show bool, format string, a ...any) {
	l := "    " + strings.TrimRight(fmt.Sprintf(format, a...), "\n")
	o.mu.Lock()
	defer o.mu.Unlock()
	if show || o.shown || o.title == "" { // a line outside any step is always shown
		o.show()
		fmt.Fprintln(o.w, l)
		return
	}
	o.pending = append(o.pending, l)
}

// show prints the heading and what waited under it (once). Callers hold mu.
func (o *Output) show() {
	if o.shown {
		return
	}
	o.shown = true
	if o.title != "" {
		fmt.Fprintf(o.w, "\n\033[1m==> %s\033[0m\n", o.title)
	}
	for _, l := range o.pending {
		fmt.Fprintln(o.w, l)
	}
	o.pending = nil
}

// finish closes the current step, counting it if it stayed hidden.
func (o *Output) finish() {
	if o.title != "" && !o.shown && !o.always {
		o.hidden++
	}
}
