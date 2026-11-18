package deps

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"

	"github.com/sideways-works/pipelinegen-sbom-go/goid"
)

func (r *reader) tools(mains []*mainModule, works []*workspace) {
	gov, tc := map[string]string{}, map[string]string{}

	for _, m := range mains {
		if m.file.Go != nil {
			gov[m.dir] = m.file.Go.Version
		}

		if m.file.Toolchain != nil {
			tc[m.dir] = m.file.Toolchain.Name
		}
	}

	for _, w := range works {
		if w.work.Go != nil {
			gov[w.file] = w.work.Go.Version
		}

		if w.work.Toolchain != nil {
			tc[w.file] = w.work.Toolchain.Name
		}
	}

	r.tool(goid.GoVersion, gov)
	r.tool(goid.Toolchain, tc)
}

// tool adds one toolchain unit over what each file says. Its version, the
// highest, is for people to read.
func (r *reader) tool(id string, byFile map[string]string) {
	if len(byFile) == 0 {
		return
	}

	h := sha256.New()
	version := ""

	for _, f := range sortedKeys(byFile) {
		h.Write([]byte(f + "\x00" + byFile[f] + "\x00"))

		if byFile[f] > version {
			version = byFile[f]
		}
	}

	r.g.Tools = append(r.g.Tools, Tool{ID: id, Version: version, Digest: hex.EncodeToString(h.Sum(nil))})
}

var (
	sdkDownload = regexp.MustCompile(`go_sdk\.download\(([^)]*)\)`)
	sdkVersion  = regexp.MustCompile(`version\s*=\s*"([^"]+)"`)
)

// bazelSDK adds the Go SDK MODULE.bazel downloads: the compiler Bazel
// builds with, which may differ from the go directive.
func (r *reader) bazelSDK() error {
	b, ok, err := r.read("MODULE.bazel")
	if err != nil || !ok {
		return err
	}

	versions := map[string]string{}

	for i, call := range sdkDownload.FindAllSubmatch(b, -1) {
		if v := sdkVersion.FindSubmatch(call[1]); v != nil {
			versions[fmt.Sprintf("MODULE.bazel#%d", i)] = string(v[1])
		}
	}

	r.tool(goid.BazelSDK, versions)

	return nil
}

// links reads each dependency's own go.mod from the module cache: the
// module graph among the dependencies. A go.mod missing from the cache is
// "can't tell" for that module, never an empty list.
func (r *reader) links(cache string) {
	units := map[string]bool{}
	for _, m := range r.g.Modules {
		units[m.Path] = true
	}

	for _, m := range r.g.Modules {
		built := module.Version{Path: m.Path, Version: m.Version}
		if m.Replace != "" {
			p, v, _ := cutVersion(m.Replace)
			built = module.Version{Path: p, Version: v}
		}

		reqs, err := requirements(cache, built)
		if err != nil {
			r.g.CannotTell = append(r.g.CannotTell, Missing{ID: m.ID, Reason: err.Error()})

			continue
		}

		for _, p := range reqs {
			if units[p] && p != m.Path {
				r.g.Links = append(r.g.Links, Link{From: m.ID, To: goid.Dep(p)})
			}
		}
	}

	slices.SortFunc(r.g.Links, func(a, b Link) int {
		if a.From != b.From {
			return strings.Compare(a.From, b.From)
		}

		return strings.Compare(a.To, b.To)
	})
}

var ErrNoModFile = errors.New("not in the module cache")

func requirements(cache string, v module.Version) ([]string, error) {
	if cache == "" {
		return nil, fmt.Errorf("%s@%s: %w: no module cache", v.Path, v.Version, ErrNoModFile)
	}

	ep, err := module.EscapePath(v.Path)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrRead, err)
	}

	ev, err := module.EscapeVersion(v.Version)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrRead, err)
	}

	p := filepath.Join(cache, "cache", "download", filepath.FromSlash(ep), "@v", ev+".mod")

	b, err := os.ReadFile(p) //nolint:gosec // a file of the module cache, by module path and version
	if err != nil {
		return nil, fmt.Errorf("%s@%s: %w", v.Path, v.Version, ErrNoModFile)
	}

	f, err := modfile.ParseLax(p, b, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrRead, err)
	}

	out := make([]string, 0, len(f.Require))
	for _, req := range f.Require {
		out = append(out, req.Mod.Path)
	}

	return out, nil
}

func cutVersion(pv string) (string, string, bool) {
	i := len(pv) - 1
	for i >= 0 && pv[i] != '@' {
		i--
	}

	if i < 0 {
		return pv, "", false
	}

	return pv[:i], pv[i+1:], true
}
