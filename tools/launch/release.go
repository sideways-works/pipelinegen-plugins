package launch

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// Repository is where the plugins' releases are published.
const Repository = "sideways-works/pipelinegen-plugins"

// EnvPipelinegenSource names a checkout of pipelinegen whose release command and pipelinegen the
// targets build instead of the ones tools/go.mod requires: a release of pipelinegen not yet out.
const EnvPipelinegenSource = "PIPELINEGEN_SOURCE"

var version = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(-rc\.[0-9]+)?$`)

var errNoVersion = fmt.Errorf("release: no version (v1.2.3, or v1.2.3-rc.1 for a candidate)")

// Release runs one form of a plugin's release with pipelinegen's release command, which releases the
// module in the plugin's directory from its tag KIND/NAME/VERSION:
//
//	build OUT [VERSION]           build the tag into OUT, twice and byte for byte alike; without
//	                              VERSION, the plugin's newest tag on HEAD
//	notice                        write the NOTICE the plugin's release needs
//	publish VERSION OUT           attach OUT to the tag's release
//	check VERSION                 fail unless the tag's published release is a fresh build of it
//	patch VERSION OUT             tag main's head, build, publish and check the release by download
//	tagged OUT CONTEXT...         the release job: the plugin's version tag on HEAD, once one of the
//	                              commit statuses CONTEXT passed, built into OUT and published
func (l *Launch) Release(ctx context.Context, dir string, args []string) (int, error) {
	dir = filepath.ToSlash(filepath.Clean(dir))

	kind, name, ok := strings.Cut(dir, "/")
	if !ok || !slices.Contains(Kinds, kind) || strings.Contains(name, "/") {
		return 2, fmt.Errorf("release: %s is no plugin directory (KIND/NAME)", dir) //nolint:err113 // said once, to the person
	}

	abs, err := l.unit(dir)
	if err != nil {
		return 2, err
	}

	var forward []string

	switch {
	case args[0] == cmdBuild && (len(args) == 2 || len(args) == 3):
		tag, err := l.tag(ctx, dir, args[2:])
		if err != nil {
			return 2, err
		}

		forward = []string{cmdBuild, abs, tag, l.abs(args[1])}
	case args[0] == "notice" && len(args) == 1:
		forward = []string{"notice", abs}
	case args[0] == "publish" && len(args) == 3:
		tag, err := l.tag(ctx, dir, args[1:2])
		if err != nil {
			return 2, err
		}

		forward = []string{"publish", Repository, tag, l.abs(args[2])}
	case args[0] == "check" && len(args) == 2:
		tag, err := l.tag(ctx, dir, args[1:2])
		if err != nil {
			return 2, err
		}

		forward = []string{"check", abs, Repository, tag}
	case args[0] == "patch" && len(args) == 3:
		tag, err := l.tag(ctx, dir, args[1:2])
		if err != nil {
			return 2, err
		}

		forward = []string{"patch", abs, Repository, tag, l.abs(args[2])}
	case args[0] == "tagged" && len(args) >= 3:
		forward = append([]string{"tagged", abs, Repository, l.abs(args[1])}, args[2:]...)
	default:
		return 2, errUsage
	}

	work, done, err := l.scratch("plugins-release-")
	if err != nil {
		return 1, err
	}
	defer done()

	tool, err := l.tool(ctx, work, "github.com/sideways-works/pipelinegen/ci/release")
	if err != nil {
		return 1, err
	}

	return 0, l.run(ctx, l.working, tool, forward...)
}

// tag is the plugin's tag of the version given, or without one its newest tag on HEAD.
func (l *Launch) tag(ctx context.Context, dir string, given []string) (string, error) {
	if len(given) == 1 {
		v := strings.TrimPrefix(given[0], dir+"/")
		if !version.MatchString(v) {
			return "", fmt.Errorf("%w: %s", errNoVersion, given[0])
		}

		return dir + "/" + v, nil
	}

	tags, err := l.output(ctx, "git", "-C", l.workspace, "tag", "--points-at", "HEAD", "--sort=-v:refname", "--list", dir+"/v*")
	if err != nil {
		return "", err
	}

	for t := range strings.SplitSeq(tags, "\n") {
		if version.MatchString(strings.TrimPrefix(t, dir+"/")) {
			return t, nil
		}
	}

	return "", fmt.Errorf("release: HEAD has no tag %s/vX.Y.Z; name the version", dir) //nolint:err113 // said once, to the person
}

// ReleaseTagged builds every plugin whose newest tag is on HEAD into out/KIND/NAME, as the release
// of that tag.
func (l *Launch) ReleaseTagged(ctx context.Context, out string) error {
	units, err := Units(l.workspace)
	if err != nil {
		return err
	}

	var tagged []string

	for _, u := range units {
		kind, _, _ := strings.Cut(u, "/")
		if !slices.Contains(Kinds, kind) {
			continue
		}

		tags, err := l.output(ctx, "git", "-C", l.workspace, "tag", "--points-at", "HEAD", "--list", u+"/v*")
		if err != nil {
			return err
		}

		if tags != "" {
			tagged = append(tagged, u)
		}
	}

	if len(tagged) == 0 {
		sayf(l.Stdout, "no plugin has a version tag on HEAD\n")

		return nil
	}

	return l.each(tagged, func(dir string) error {
		code, err := l.Release(ctx, dir, []string{cmdBuild, filepath.Join(out, filepath.FromSlash(dir))})
		if err == nil && code != 0 {
			err = fmt.Errorf("release %s exited %d", dir, code) //nolint:err113 // its output is above
		}

		return err
	})
}

// abs is path as the person meant it: relative to where `bazel run` was started.
func (l *Launch) abs(path string) string {
	if filepath.IsAbs(path) {
		return path
	}

	return filepath.Join(l.working, path)
}
