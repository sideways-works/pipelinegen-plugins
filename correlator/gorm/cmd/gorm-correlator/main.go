// Command gorm-correlator is a pipelinegen plugin that links Go declarations
// to the PostgreSQL relations they reach through GORM.
package main

import (
	"context"
	"os"

	"github.com/sideways-works/pipelinegen/plugin"

	"github.com/sideways-works/pipelinegen-correlator-gorm/internal/correlator"
)

// Version is stamped by the release build; a "dev" build keeps no parse
// cache.
var Version = "dev"

func main() {
	// The SQL parser's runtime keeps a handle on os.Stdout; the answer
	// goes to the real stdout, and anything the parser prints to stderr.
	out := os.Stdout
	os.Stdout = os.Stderr

	ctx := context.Background()
	v := Version

	call := func(f func(context.Context, []byte, string) (any, error)) plugin.Handler {
		return func(in []byte) (any, error) { return f(ctx, in, v) }
	}

	os.Exit(plugin.Serve(os.Args[1:], os.Stdin, out, os.Stderr, map[string]plugin.Handler{
		plugin.CallDescribe:    func([]byte) (any, error) { return correlator.Describe(v), nil },
		plugin.CallDetect:      call(correlator.Detect),
		plugin.CallLinks:       call(correlator.Links),
		plugin.CallWhy:         call(correlator.Why),
		correlator.CallInspect: call(correlator.Inspect),
	}))
}
