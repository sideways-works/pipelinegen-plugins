package launch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// Repository is where the plugins' releases are published.
const Repository = "sideways-works/pipelinegen-plugins"

var version = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(-rc\.[0-9]+)?$`)

// Release runs one form of a plugin's release with pipelinegen's release command, which tools/go.mod
// pins. That command releases a repository whose root is the plugin, so each form hands it a split:
// a repository named pipelinegen-KIND-NAME (which names the program) whose one commit holds the
// plugin's directory at the commit released, tagged VERSION, and SUB/VERSION for each nested module.
// A plugin's release is tagged KIND/NAME/VERSION here.
//
//	build OUT [VERSION]  build the plugin into OUT, twice and byte for byte alike: the commit of the
//	                     tag KIND/NAME/VERSION, or HEAD when there is no such tag; without VERSION,
//	                     the newest tag of the plugin on HEAD
//	notice               write the NOTICE the plugin's release needs, from its working tree
//	publish VERSION OUT  attach OUT to the release of KIND/NAME/VERSION, then download it and
//	                     compare every file with OUT
//	check VERSION        build KIND/NAME/VERSION afresh and compare every published file with it:
//	                     a released file that a fresh build of its tag doesn't give fails
func (l *Launch) Release(ctx context.Context, dir string, args []string) (int, error) {
	dir = filepath.ToSlash(filepath.Clean(dir))

	kind, name, ok := strings.Cut(dir, "/")
	if !ok || !slices.Contains(Kinds, kind) || strings.Contains(name, "/") {
		return 2, fmt.Errorf("release: %s is no plugin directory (KIND/NAME)", dir) //nolint:err113 // said once, to the person
	}

	if _, err := l.unit(dir); err != nil {
		return 2, err
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

	r := &release{Launch: l, dir: dir, repo: "pipelinegen-" + kind + "-" + name, work: work, tool: tool}

	switch {
	case args[0] == cmdBuild && (len(args) == 2 || len(args) == 3):
		v := ""
		if len(args) == 3 {
			v = args[2]
		}

		return 0, r.build(ctx, l.abs(args[1]), v)
	case args[0] == "notice" && len(args) == 1:
		return 0, r.notice(ctx)
	case args[0] == "publish" && len(args) == 3:
		return 0, r.publish(ctx, args[1], l.abs(args[2]))
	case args[0] == "check" && len(args) == 2:
		return 0, r.check(ctx, args[1])
	}

	return 2, errUsage
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

type release struct {
	*Launch

	dir, repo, work, tool string
}

func (r *release) tag(v string) string { return r.dir + "/" + v }

func (r *release) git(ctx context.Context, args ...string) (string, error) {
	return r.output(ctx, "git", append([]string{"-C", r.workspace}, args...)...)
}

// commit is the commit a build of v takes, and the version it is built as: the commit of the tag
// KIND/NAME/v when there is one, else HEAD; without v, the newest tag of the plugin on HEAD.
func (r *release) commit(ctx context.Context, v string) (string, string, error) {
	if v == "" {
		tags, err := r.git(ctx, "tag", "--points-at", "HEAD", "--sort=-v:refname", "--list", r.dir+"/v*")
		if err != nil {
			return "", "", err
		}

		for t := range strings.SplitSeq(tags, "\n") {
			if version.MatchString(strings.TrimPrefix(t, r.dir+"/")) {
				v = strings.TrimPrefix(t, r.dir+"/")

				break
			}
		}

		if v == "" {
			return "", "", fmt.Errorf("release: HEAD has no tag %s/vX.Y.Z; name the version to build it as", r.dir) //nolint:err113 // said once, to the person
		}
	}

	if !version.MatchString(v) {
		return "", "", fmt.Errorf("release: %s is no version (v1.2.3, or v1.2.3-rc.1 for a candidate)", v) //nolint:err113 // said once, to the person
	}

	rev := "HEAD"
	if _, err := r.git(ctx, "rev-parse", "-q", "--verify", "refs/tags/"+r.tag(v)+"^{commit}"); err == nil {
		rev = "refs/tags/" + r.tag(v)
	}

	commit, err := r.git(ctx, "rev-parse", rev+"^{commit}")

	return commit, v, err
}

// split makes the repository the release command builds: in a directory named pipelinegen-KIND-NAME,
// one commit whose tree is the plugin's directory at commit, with that commit's authors and dates,
// so the same commit always gives the same split, tagged v and SUB/v for each nested module.
func (r *release) split(ctx context.Context, commit, v string) (string, error) {
	dest := filepath.Join(r.work, "split", r.repo)
	if err := os.RemoveAll(dest); err != nil {
		return "", fmt.Errorf("release: %w", err)
	}

	meta, err := r.git(ctx, "show", "-s", "--format=%an%x00%ae%x00%aI%x00%cn%x00%ce%x00%cI", commit)
	if err != nil {
		return "", err
	}

	f := strings.Split(meta, "\x00")
	if len(f) != 6 {
		return "", fmt.Errorf("release: can't read the commit %s", commit) //nolint:err113 // said once, to the person
	}

	in := func(args ...string) (string, error) {
		return r.output(ctx, "git", append([]string{"-C", dest}, args...)...)
	}

	if _, err := r.output(ctx, "git", "init", "-q", "-b", "main", dest); err != nil {
		return "", err
	}

	if _, err := in("fetch", "-q", "--no-tags", r.workspace, commit); err != nil {
		return "", err
	}

	cmd := r.command(ctx, dest, append(r.env(),
		"GIT_AUTHOR_NAME="+f[0], "GIT_AUTHOR_EMAIL="+f[1], "GIT_AUTHOR_DATE="+f[2],
		"GIT_COMMITTER_NAME="+f[3], "GIT_COMMITTER_EMAIL="+f[4], "GIT_COMMITTER_DATE="+f[5],
	), "git", "commit-tree", "-m", r.dir+" at "+commit, commit+":"+r.dir)

	var out bytes.Buffer

	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("release: split %s: %w", r.dir, err)
	}

	split := strings.TrimSpace(out.String())

	if _, err := in("reset", "-q", "--hard", split); err != nil {
		return "", err
	}

	mods, err := Modules(dest)
	if err != nil {
		return "", err
	}

	tags := []string{v}

	for _, m := range mods {
		if m != "." {
			tags = append(tags, m+"/"+v)
		}
	}

	for _, t := range tags {
		if _, err := in("tag", t, split); err != nil {
			return "", err
		}
	}

	return dest, nil
}

func (r *release) runTool(ctx context.Context, args ...string) error {
	return r.run(ctx, r.working, r.tool, args...)
}

func (r *release) build(ctx context.Context, out, v string) error {
	commit, v, err := r.commit(ctx, v)
	if err != nil {
		return err
	}

	src, err := r.split(ctx, commit, v)
	if err != nil {
		return err
	}

	sayf(r.Stderr, "building %s %s from %s\n", r.dir, v, commit)

	return r.runTool(ctx, "build", src, v, out)
}

func (r *release) notice(ctx context.Context) error {
	dest := filepath.Join(r.work, "notice", r.repo)
	abs := filepath.Join(r.workspace, filepath.FromSlash(r.dir))

	files, err := r.output(ctx, "git", "-C", abs, "ls-files", "-co", "--exclude-standard", "-z", ".")
	if err != nil {
		return err
	}

	for f := range strings.SplitSeq(files, "\x00") {
		if f == "" {
			continue
		}

		if err := copyFile(filepath.Join(abs, f), filepath.Join(dest, f)); err != nil {
			return err
		}
	}

	if err := r.runTool(ctx, "notice", dest); err != nil {
		return err
	}

	return copyFile(filepath.Join(dest, "NOTICE"), filepath.Join(abs, "NOTICE"))
}

func (r *release) publish(ctx context.Context, v, out string) error {
	if !version.MatchString(v) {
		return fmt.Errorf("release: %s is no version (v1.2.3, or v1.2.3-rc.1 for a candidate)", v) //nolint:err113 // said once, to the person
	}

	if _, err := r.git(ctx, "rev-parse", "-q", "--verify", "refs/tags/"+r.tag(v)+"^{commit}"); err != nil {
		return fmt.Errorf("release: there is no tag %s", r.tag(v)) //nolint:err113 // said once, to the person
	}

	if err := r.runTool(ctx, "publish", Repository, r.tag(v), out); err != nil {
		return err
	}

	return r.compare(ctx, v, out)
}

func (r *release) check(ctx context.Context, v string) error {
	if !version.MatchString(v) {
		return fmt.Errorf("release: %s is no version (v1.2.3, or v1.2.3-rc.1 for a candidate)", v) //nolint:err113 // said once, to the person
	}

	if _, err := r.git(ctx, "rev-parse", "-q", "--verify", "refs/tags/"+r.tag(v)+"^{commit}"); err != nil {
		return fmt.Errorf("release: there is no tag %s", r.tag(v)) //nolint:err113 // said once, to the person
	}

	fresh := filepath.Join(r.work, "fresh")
	if err := r.build(ctx, fresh, v); err != nil {
		return err
	}

	return r.compare(ctx, v, fresh)
}

// compare downloads the published release of v and fails unless it holds exactly the files of
// built, byte for byte.
func (r *release) compare(ctx context.Context, v, built string) error {
	published := filepath.Join(r.work, "published")

	if err := r.run(ctx, r.work, "gh", "release", "download", r.tag(v), "-R", Repository, "-D", published); err != nil {
		return err
	}

	if err := SameFiles(published, built); err != nil {
		return fmt.Errorf("release: the release of %s: %w", r.tag(v), err)
	}

	sayf(r.Stdout, "%s: every published file is byte for byte the build's\n", r.tag(v))

	return nil
}

var errDiffer = errors.New("differs")

// SameFiles fails unless the directories a and b hold the same file names with the same bytes.
func SameFiles(a, b string) error {
	names := func(dir string) ([]string, error) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil, fmt.Errorf("compare: %w", err)
		}

		var out []string
		for _, e := range entries {
			out = append(out, e.Name())
		}

		return out, nil
	}

	na, err := names(a)
	if err != nil {
		return err
	}

	nb, err := names(b)
	if err != nil {
		return err
	}

	if !slices.Equal(na, nb) {
		return fmt.Errorf("%w: files %v, the build's %v", errDiffer, na, nb)
	}

	var errs []error

	for _, n := range na {
		x, errA := os.ReadFile(filepath.Join(a, n)) //nolint:gosec // a release's own file
		y, errB := os.ReadFile(filepath.Join(b, n)) //nolint:gosec // a build's own file

		if err := errors.Join(errA, errB); err != nil {
			return fmt.Errorf("compare: %w", err)
		}

		if !bytes.Equal(x, y) {
			errs = append(errs, fmt.Errorf("%w: %s", errDiffer, n))
		}
	}

	return errors.Join(errs...)
}

func copyFile(from, to string) error {
	data, err := os.ReadFile(from) //nolint:gosec // a file of the repository
	if err != nil {
		return fmt.Errorf("copy: %w", err)
	}

	info, err := os.Stat(from)
	if err != nil {
		return fmt.Errorf("copy: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(to), 0o750); err != nil {
		return fmt.Errorf("copy: %w", err)
	}

	if err := os.WriteFile(to, data, info.Mode().Perm()); err != nil { //nolint:gosec // a copy into the release's scratch directory, or the plugin's NOTICE
		return fmt.Errorf("copy: %w", err)
	}

	return nil
}
