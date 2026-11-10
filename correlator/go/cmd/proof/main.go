// Command proof measures a narrowing on a real change of a repository that
// uses go-correlator: it walks every recorded slice through the link sets
// of both trees with the narrowing on and off, and compares the walk with
// the plan the release before made of the same change.
//
//	go run ./cmd/proof -repo <repository> -base <commit> -head <commit> \
//	  -lists <lists> -plan <plan.json> [-modules backend] [-json walk.json]
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/sideways-works/pipelinegen-correlator-go/internal/proof"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	o, out, ok := options(args)
	if !ok {
		return 2
	}

	work, err := os.MkdirTemp("", "proof-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "proof:", err)

		return 1
	}

	defer os.RemoveAll(work) //nolint:errcheck // a scratch directory

	o.Work = work

	rep, err := proof.Run(context.Background(), o)
	if err != nil {
		fmt.Fprintln(os.Stderr, "proof:", err)

		return 1
	}

	rep.Write(os.Stdout)

	if out != "" {
		b, err := json.MarshalIndent(rep, "", " ")
		if err == nil {
			err = os.WriteFile(out, b, 0o600)
		}

		if err != nil {
			fmt.Fprintln(os.Stderr, "proof:", err)

			return 1
		}
	}

	return 0
}

func options(args []string) (proof.Options, string, bool) {
	fs := flag.NewFlagSet("proof", flag.ContinueOnError)

	var (
		o                     proof.Options
		modules, out, explain string
	)

	fs.StringVar(&o.Repo, "repo", ".", "the git repository holding both commits; it is only read")
	fs.StringVar(&o.Base, "base", "", "the change's base commit")
	fs.StringVar(&o.Head, "head", "", "the change's head commit")
	fs.StringVar(&o.Lists, "lists", "", "the recorded lists: <suite>/<slice>.txt, a unit id per line")
	fs.StringVar(&o.Plan, "plan", "", "the release's plan of the same change (pipelinegen affected --json)")
	fs.StringVar(&o.Narrowing, "narrowing", "", "the narrowing to measure (default interface-methods)")
	fs.StringVar(&modules, "modules", "", "the module directories to read, comma-separated (default every one)")
	fs.StringVar(&o.Cache, "cache", "", "a directory that keeps what dependencies' sources hold between runs")
	fs.BoolVar(&o.AnyLink, "any-link", false, "count a node changed by any link only one tree has, as the planner did before v0.1.13-rc.6")
	fs.StringVar(&explain, "explain", "", "nodes whose differing links to list, comma-separated")
	fs.StringVar(&out, "json", "", "also write the report, slice by slice, to this file")

	if err := fs.Parse(args); err != nil {
		return o, "", false
	}

	if o.Base == "" || o.Head == "" || o.Lists == "" || o.Plan == "" {
		fmt.Fprintln(os.Stderr, "proof: -base, -head, -lists and -plan are needed")

		return o, "", false
	}

	if explain != "" {
		o.Explain = strings.Split(explain, ",")
	}

	if modules != "" {
		o.Modules = strings.Split(modules, ",")
	}

	return o, out, true
}
