// Command coverfloor checks a coverage profile against floors per package:
//
//	go run ./scripts/coverfloor coverage.out scripts/coverage-floors.txt
//
// The profile may come from several test binaries (go test -coverpkg):
// a block counts as covered if any of them ran it.
package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
)

const module = "github.com/davitizhgenti/hostd/"

type floor struct {
	pattern string
	min     float64
}

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) != 2 {
		fmt.Fprintln(stderr, "usage: coverfloor PROFILE FLOORS")
		return 2
	}
	cov, err := readProfile(args[0])
	if err != nil {
		fmt.Fprintln(stderr, "coverfloor:", err)
		return 2
	}
	floors, err := readFloors(args[1])
	if err != nil {
		fmt.Fprintln(stderr, "coverfloor:", err)
		return 2
	}
	pkgs := make([]string, 0, len(cov))
	for p := range cov {
		pkgs = append(pkgs, p)
	}
	sort.Strings(pkgs)
	failed := 0
	for _, p := range pkgs {
		pct := cov[p].pct()
		f := floorFor(p, floors)
		mark := "  "
		if pct < f.min {
			mark = "✗ "
			failed++
		}
		fmt.Fprintf(stdout, "%s%5.1f%%  (floor %2.0f%%)  %s\n", mark, pct, f.min, p)
	}
	if failed > 0 {
		fmt.Fprintf(stdout, "\n%d package(s) below their coverage floor (%s)\n", failed, args[1])
		return 1
	}
	fmt.Fprintln(stdout, "\nevery package is at or above its coverage floor")
	return 0
}

type counts struct{ covered, total int }

func (c counts) pct() float64 {
	if c.total == 0 {
		return 100
	}
	return 100 * float64(c.covered) / float64(c.total)
}

// readProfile sums a profile's statements per package, a block covered
// if any test binary ran it.
func readProfile(name string) (map[string]counts, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	type block struct {
		stmts int
		hit   bool
	}
	blocks := map[string]*block{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "mode:") || line == "" {
			continue
		}
		// file.go:12.3,14.5 2 1
		loc, rest, ok := strings.Cut(line, " ")
		fields := strings.Fields(rest)
		if !ok || len(fields) != 2 {
			return nil, fmt.Errorf("%s: unexpected line %q", name, line)
		}
		stmts, err1 := strconv.Atoi(fields[0])
		count, err2 := strconv.Atoi(fields[1])
		if err1 != nil || err2 != nil {
			return nil, fmt.Errorf("%s: unexpected line %q", name, line)
		}
		b := blocks[loc]
		if b == nil {
			b = &block{stmts: stmts}
			blocks[loc] = b
		}
		b.hit = b.hit || count > 0
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	out := map[string]counts{}
	for loc, b := range blocks {
		file, _, _ := strings.Cut(loc, ":")
		pkg := strings.TrimPrefix(path.Dir(file), module)
		c := out[pkg]
		c.total += b.stmts
		if b.hit {
			c.covered += b.stmts
		}
		out[pkg] = c
	}
	return out, nil
}

func readFloors(name string) ([]floor, error) {
	b, err := os.ReadFile(name)
	if err != nil {
		return nil, err
	}
	var out []floor
	for i, line := range strings.Split(string(b), "\n") {
		line, _, _ = strings.Cut(line, "#")
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		pct, err := strconv.ParseFloat(strings.TrimSuffix(f[len(f)-1], "%"), 64)
		if len(f) != 2 || err != nil {
			return nil, fmt.Errorf("%s:%d: want PATTERN PERCENT", name, i+1)
		}
		out = append(out, floor{f[0], pct})
	}
	return out, nil
}

// floorFor is the most specific floor for pkg: its own, then the longest
// prefix/..., then *.
func floorFor(pkg string, floors []floor) floor {
	best, bestLen := floor{"*", 0}, -1
	for _, f := range floors {
		n := -1
		switch {
		case f.pattern == pkg:
			n = 1 << 30
		case strings.HasSuffix(f.pattern, "/..."):
			pre := strings.TrimSuffix(f.pattern, "/...")
			if pkg == pre || strings.HasPrefix(pkg, pre+"/") {
				n = len(pre)
			}
		case f.pattern == "*":
			n = 0
		}
		if n > bestLen {
			best, bestLen = f, n
		}
	}
	return best
}
