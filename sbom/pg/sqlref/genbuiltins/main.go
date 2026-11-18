// Command genbuiltins writes the names of PostgreSQL's built-in functions,
// one per line under a header naming the version, from the catalog source
// pg_proc.dat read on stdin:
//
//	curl -sfL https://raw.githubusercontent.com/postgres/postgres/REL_17_STABLE/src/include/catalog/pg_proc.dat |
//	  go run ./sqlref/genbuiltins -version 17 > sqlref/builtins.txt
package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
)

var proname = regexp.MustCompile(`proname => '([^']+)'`)

func main() {
	version := flag.String("version", "", "the PostgreSQL major version the catalog source is from")

	flag.Parse()

	if err := run(os.Stdin, os.Stdout, *version); err != nil {
		if _, werr := fmt.Fprintln(os.Stderr, err); werr != nil {
			os.Exit(2)
		}

		os.Exit(1)
	}
}

func run(in io.Reader, out io.Writer, version string) error {
	names := map[string]bool{}
	sc := bufio.NewScanner(in)

	for sc.Scan() {
		for _, m := range proname.FindAllStringSubmatch(sc.Text(), -1) {
			names[m[1]] = true
		}
	}

	if err := sc.Err(); err != nil {
		return fmt.Errorf("reading pg_proc.dat: %w", err)
	}

	sorted := make([]string, 0, len(names))
	for n := range names {
		sorted = append(sorted, n)
	}

	sort.Strings(sorted)

	w := bufio.NewWriter(out)

	if _, err := fmt.Fprintf(w, "# The names of PostgreSQL %s's built-in functions (every proname in src/include/catalog/pg_proc.dat), written by genbuiltins.\n", version); err != nil {
		return fmt.Errorf("writing: %w", err)
	}

	for _, n := range sorted {
		if _, err := fmt.Fprintln(w, n); err != nil {
			return fmt.Errorf("writing: %w", err)
		}
	}

	if err := w.Flush(); err != nil {
		return fmt.Errorf("writing: %w", err)
	}

	return nil
}
