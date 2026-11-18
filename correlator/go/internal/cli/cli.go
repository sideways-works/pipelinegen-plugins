package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/sideways-works/pipelinegen-plugins/correlator/go/collect"
	"github.com/sideways-works/pipelinegen-plugins/correlator/go/graph"
	"github.com/sideways-works/pipelinegen-plugins/correlator/go/observe"
	"github.com/sideways-works/pipelinegen-sbom-go/decls"
	"github.com/sideways-works/pipelinegen-sbom-go/detect"
	"github.com/sideways-works/pipelinegen/plugin"
)

// Exit codes the protocol defines.
const (
	exitOK         = 0
	exitFailed     = 1
	exitCannotTell = 3
)

const (
	callDescribe = "describe"
	callDetect   = "detect"
	callBind     = "bind"
	callObserve  = "observe"
	callCollect  = "collect"
	callWhy      = "why"
	// callLinks answers a package's links per set; the planner walks them
	// in place of bind.
	callLinks = "links"
)

type Config struct {
	// Modules are the directories of the Go modules to read; none reads
	// every go.mod of the tree.
	Modules []string `json:"modules"`
	// PureCalls are the functions an initialiser may call and still be
	// bound through references only; none is the default list.
	PureCalls []string    `json:"pure_calls"`
	Build     decls.Build `json:"build"`
	// CoverDir is the directory a stack's binaries talk through; empty is
	// PIPELINEGEN_COVER_DIR.
	CoverDir string `json:"cover_dir"`
	// Stack says the suite's batches start long-running binaries: collect
	// then fails when none is ready.
	Stack bool `json:"stack"`
	// WaitSeconds bounds collect's wait for the binaries (8 when 0).
	WaitSeconds float64 `json:"wait_seconds"`
}

// Request is what every call but describe reads on stdin; each call reads
// its own fields.
type Request struct {
	Protocol   int      `json:"protocol"`
	Work       string   `json:"work"`
	Cache      string   `json:"cache"`
	Tree       string   `json:"tree"`
	Config     Config   `json:"config"`
	Narrowings []string `json:"narrowings"`
	Paths      []string `json:"paths"`
	// Claimed are the files the core passes as this plugin's, after the
	// repository's include and exclude patterns.
	Claimed []Claimed `json:"claimed"`

	// bind
	Widths []string            `json:"widths"`
	Slices []Slice             `json:"slices"`
	Roots  map[string][]string `json:"roots"`
	Setup  map[string][]string `json:"setup"`

	// observe
	Slice string   `json:"slice"`
	Dirs  []string `json:"dirs"`

	// collect
	Action string `json:"action"`
	Dir    string `json:"dir"`
	// Stack says a stack runs, like the config's stack.
	Stack bool `json:"stack"`

	// why
	From string `json:"from"`
	To   string `json:"to"`
}

type Claimed struct {
	Path string `json:"path"`
	Role string `json:"role"`
}

type Slice struct {
	Suite   string   `json:"suite"`
	Name    string   `json:"name"`
	Sources []string `json:"sources"`
	Tests   []string `json:"tests"`
	// List is nil for a slice without a list yet.
	List  []string `json:"list"`
	Depth int      `json:"depth"`
}

var (
	errUsage   = errors.New("usage: go-correlator describe|detect|bind|observe|collect|why < request.json")
	errRequest = errors.New("request")
	// errCannotTell is an answer of its own: the files can't tell.
	errCannotTell = errors.New("can't tell")
	// errUncacheable marks a can't-tell that depends on the machine, so
	// the answer says "cacheable": false.
	errUncacheable = errors.New("not cacheable")
)

func Run(args []string, stdin io.Reader, stdout, stderr io.Writer, version string) int {
	if len(args) != 1 {
		say(stderr, errUsage)

		return exitFailed
	}

	answer, err := call(context.Background(), args[0], stdin, stderr, version)
	if err != nil {
		say(stderr, "go-correlator:", err)

		if errors.Is(err, errCannotTell) {
			if errors.Is(err, errUncacheable) {
				uncacheable(stdout, stderr)
			}

			return exitCannotTell
		}

		return exitFailed
	}

	if err := json.NewEncoder(stdout).Encode(answer); err != nil {
		say(stderr, "go-correlator:", err)

		return exitFailed
	}

	return exitOK
}

func call(ctx context.Context, name string, stdin io.Reader, stderr io.Writer, version string) (any, error) {
	if name == callDescribe {
		return describe(ctx, version, stderr), nil
	}

	var req Request
	if err := json.NewDecoder(stdin).Decode(&req); err != nil {
		return nil, fmt.Errorf("%w: %w", errRequest, err)
	}

	if req.Protocol != 1 {
		return nil, fmt.Errorf("%w: protocol %d, this plugin speaks 1", errRequest, req.Protocol)
	}

	switch name {
	case callDetect:
		return detectCall(req)
	case callBind:
		return bind(ctx, req, stderr)
	case callLinks:
		return links(ctx, req, stderr)
	case callObserve:
		return observeCall(ctx, req)
	case callCollect:
		return collectCall(ctx, req, stderr)
	case callWhy:
		return why(ctx, req)
	default:
		return nil, fmt.Errorf("%w: unknown call %q", errUsage, name)
	}
}

func uncacheable(stdout, stderr io.Writer) {
	no := false
	if err := json.NewEncoder(stdout).Encode(plugin.Answer{Cacheable: &no}); err != nil {
		say(stderr, "go-correlator:", err)
	}
}

// say writes a line for people; a failed write has nobody left to tell.
func say(w io.Writer, a ...any) {
	if _, err := fmt.Fprintln(w, a...); err != nil {
		return
	}
}

// describe says what the plugin is. Its toolchains are the go its commands
// resolve in the core's working directory; a go that can't be resolved is
// left out, never a failure, and bind says why it can't tell.
func describe(ctx context.Context, version string, stderr io.Writer) plugin.Description {
	ns := make([]plugin.Narrowing, 0, len(graph.Narrowings))
	for _, n := range graph.Narrowings {
		ns = append(ns, plugin.Narrowing{Name: n.Name, OffAt: n.OffAt, Note: n.Note})
	}

	return plugin.Description{
		Protocol: plugin.Protocol, Kind: plugin.KindCorrelator, Name: "go-correlator", Version: version,
		Capabilities: []string{callDetect, callBind, callLinks, callObserve, callCollect, callWhy},
		Candidates:   candidates,
		Inputs:       map[string]string{callDetect: plugin.PerFile, callBind: plugin.PerSet, callLinks: plugin.PerSet},
		Narrowings:   ns,
		// the type checker runs the go command, at least the version the
		// tree's go.mod asks for
		Needs:      []string{"go>=go.mod"},
		Toolchains: toolchains(ctx, stderr),
		Caches:     []string{"gocache", "gomodcache"},
		// on a person's machine the core fills the module cache before
		// planning; the plugin itself never reaches the network
		Fill: map[string]plugin.Fill{"gomodcache": {Files: []string{goMod, goSum}, Run: []string{"go", "mod", "download"}}},
	}
}

func toolchains(ctx context.Context, stderr io.Writer) map[string]string {
	v, err := graph.GoVersion(ctx, "", decls.Build{})
	if err != nil {
		say(stderr, "go-correlator: no go toolchain to report:", err)

		return nil
	}

	return map[string]string{"go": v}
}

func load(ctx context.Context, req Request) (*graph.Tree, error) {
	return loadPackages(ctx, req, nil)
}

func loadPackages(ctx context.Context, req Request, dirs []string) (*graph.Tree, error) {
	if req.Tree == "" {
		return nil, fmt.Errorf("%w: no tree", errRequest)
	}

	t, err := graph.Load(ctx, req.Tree, graph.Options{
		Modules: req.Config.Modules, PureCalls: req.Config.PureCalls, Build: req.Config.Build, Packages: dirs,
		Claimed: claimedFiles(req.Claimed),
	})
	if errors.Is(err, graph.ErrToolchain) {
		// the machine's go command, not the tree: asking again with the
		// right one answers
		return nil, fmt.Errorf("%w: %w: %w", errCannotTell, errUncacheable, err)
	}

	if errors.Is(err, graph.ErrLoad) || errors.Is(err, graph.ErrModule) {
		// a tree the type checker can't read (code that doesn't compile,
		// a module the cache lacks) can't be bound: the graph alone
		// applies, which binds wide
		return nil, fmt.Errorf("%w: %w", errCannotTell, err)
	}

	if err != nil {
		return nil, fmt.Errorf("load: %w", err)
	}

	return t, nil
}

// readNames reads N for the tree, and says once on stderr what binds whole
// method sets.
func readNames(ctx context.Context, req Request, stderr io.Writer) (*graph.Names, error) {
	n, err := graph.ReadNames(ctx, req.Tree, graph.NamesOptions{Modules: req.Config.Modules, Build: req.Config.Build, Cache: req.Cache})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errCannotTell, err)
	}

	handers := map[string][]string{}

	for _, from := range sortedKeys(n.Hands) {
		for _, p := range n.Hands[from] {
			handers[p] = append(handers[p], strings.TrimPrefix(strings.TrimPrefix(from, "group:"), "node:go:test:"))
		}
	}

	for _, p := range sortedKeys(handers) {
		say(stderr, fmt.Sprintf("go-correlator: %s reaches methods in a way no name tells (%s): what the tree hands to it "+
			"binds its whole method set, in the binaries of %d packages", p, strings.Join(n.Wide[p], ", "), len(slices.Compact(handers[p]))))
	}

	if n.Uncacheable != "" {
		say(stderr, "go-correlator: "+n.Uncacheable+"; this answer is not cached")
	}

	return n, nil
}

func sayForced(stderr io.Writer, t *graph.Tree) {
	for _, why := range t.Forced() {
		say(stderr, "go-correlator: interface-methods binds every type's whole method set: "+why)
	}

	for _, at := range t.Executes() {
		say(stderr, "go-correlator: a template whose text the scan did not read is executed at "+at+
			": what it is handed binds its whole method set wherever that code is linked")
	}
}

func observeCall(ctx context.Context, req Request) (any, error) {
	if req.Tree == "" || len(req.Dirs) == 0 {
		return nil, fmt.Errorf("%w: observe needs a tree and dirs", errRequest)
	}

	tree, err := decls.Root(req.Tree)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errRequest, err)
	}

	mods := req.Config.Modules
	if len(mods) == 0 && req.Claimed != nil {
		mods = claimedModules(req.Claimed)
	}

	o, err := observe.Observe(ctx, tree, "", req.Dirs, mods...)
	if errors.Is(err, observe.ErrMissing) || errors.Is(err, observe.ErrSource) {
		// evidence that is missing, or that can't be attributed to the
		// tree, mustn't become a list that leaves it out
		return nil, fmt.Errorf("%w: %w", errCannotTell, err)
	}

	if err != nil {
		return nil, fmt.Errorf("observe: %w", err)
	}

	return plugin.ObserveAnswer{Touched: nonNil(o.Touched), SetupLike: nonNil(o.SetupLike)}, nil
}

func collectCall(ctx context.Context, req Request, stderr io.Writer) (any, error) {
	type answer struct {
		Answered []string `json:"answered"`
		Gone     []string `json:"gone"`
	}

	req.Config.Stack = req.Config.Stack || req.Stack

	switch req.Action {
	case plugin.CollectSliceStart:
		// the counters were cleared by the last snapshot
		return answer{Answered: []string{}, Gone: []string{}}, nil
	case plugin.CollectSetupEnd, plugin.CollectSliceEnd:
	default:
		return nil, fmt.Errorf("%w: collect action %q", errRequest, req.Action)
	}

	if req.Dir == "" {
		return nil, fmt.Errorf("%w: collect needs a dir", errRequest)
	}

	// a task writes into the slice's directory without knowing the plugin
	// entry's name or which part of the slice runs: part 1's directory is
	// the parent of this plugin's own, a later part takes from it too
	if _, err := collect.Take(collect.SliceDir(req.Dir, req.Slice), req.Dir); err != nil {
		return nil, fmt.Errorf("collect: %w", err)
	}

	dir := req.Config.CoverDir
	if dir == "" {
		dir = os.Getenv(collect.EnvDir)
	}

	if dir == "" {
		if req.Config.Stack {
			return nil, fmt.Errorf("%w: a stack runs but neither cover_dir nor %s names its directory", errRequest, collect.EnvDir)
		}

		return answer{Answered: []string{}, Gone: []string{}}, nil
	}

	o := collect.Options{Dir: dir, Stack: req.Config.Stack, Wait: time.Duration(req.Config.WaitSeconds * float64(time.Second))}

	r, err := collect.Snapshot(ctx, o, req.Dir)
	for _, g := range r.Gone {
		say(stderr, "go-correlator: "+g+" has exited; its counters were written when it stopped, if it stopped cleanly")
	}

	if err != nil {
		return nil, fmt.Errorf("collect: %w", err)
	}

	return answer{Answered: nonNil(r.Answered), Gone: nonNil(r.Gone)}, nil
}

func why(ctx context.Context, req Request) (any, error) {
	t, err := load(ctx, req)
	if err != nil {
		return nil, err
	}

	type step struct {
		Unit string `json:"unit"`
		Note string `json:"note"`
	}

	path := []step{}
	for _, s := range t.Why(req.From, req.To) {
		path = append(path, step{Unit: s.Unit, Note: s.Note})
	}

	return struct {
		Path []step `json:"path"`
	}{Path: path}, nil
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}

	return s
}

// The module files the go command reads beside the code.
const (
	goMod  = "go.mod"
	goSum  = "go.sum"
	goWork = "go.work"
)

// candidates are the files the correlator reads: Go code and the module
// files the go command reads with it.
var candidates = []string{"**/*.go", "**/go.mod", "**/go.sum"}

// detectCall claims the request's Go files and module files; a path that
// is gone is left out, a tree that is gone fails.
func detectCall(req Request) (any, error) {
	tree, err := decls.Root(req.Tree)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errRequest, err)
	}

	out := plugin.DetectAnswer{Claims: []plugin.Claim{}}

	for _, p := range req.Paths {
		p = path.Clean(filepath.ToSlash(p))
		if b := path.Base(p); !strings.HasSuffix(b, ".go") && b != goMod && b != goSum {
			continue
		}

		content, err := os.ReadFile(filepath.Join(tree, p)) //nolint:gosec // a path of the tree the core asks about
		if err != nil {
			continue
		}

		if c, ok := detect.File(p, content); ok {
			out.Claims = append(out.Claims, plugin.Claim{Path: c.Path, Role: c.Role})
		}
	}

	return out, nil
}

func claimedFiles(claimed []Claimed) map[string]bool {
	if claimed == nil {
		return nil
	}

	out := make(map[string]bool, len(claimed))
	for _, c := range claimed {
		out[path.Clean(c.Path)] = true
	}

	return out
}

func claimedModules(claimed []Claimed) []string {
	out := []string{}

	for _, c := range claimed {
		if path.Base(c.Path) == goMod {
			out = append(out, path.Dir(path.Clean(c.Path)))
		}
	}

	slices.Sort(out)

	return slices.Compact(out)
}
