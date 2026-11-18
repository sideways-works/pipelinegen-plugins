package concourse

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

const capPackages = "packages"

// Packages is read from the request itself, not the spec, so the runner
// builds with pipelinegen releases that predate it.
type Packages struct {
	Planner *SpecPackages           `json:"planner_packages,omitempty"`
	Images  *SpecImages             `json:"images,omitempty"`
	Suites  map[string]SpecPackages `json:"-"`
}

type SpecPackages struct {
	Names []string `json:"names"`
	// Install is a POSIX shell script, run as root before the task's own command.
	Install string `json:"install"`
	// Images maps each resource the steps run in that has a cached image to its key.
	Images map[string]string `json:"images,omitempty"`
}

type SpecImages struct {
	Registry string `json:"registry"`
	// Credentials and PullCredentials name the vars holding the push and pull logins.
	Credentials     string           `json:"credentials,omitempty"`
	PullCredentials string           `json:"pull_credentials,omitempty"`
	Builds          []SpecImageBuild `json:"builds,omitempty"`
}

// SpecImageBuild is one cached image: Install run on Base, a registry-image
// resource, at Digest, pushed as Registry:Key.
type SpecImageBuild struct {
	Key     string   `json:"key"`
	Base    string   `json:"base"`
	Digest  string   `json:"digest"`
	Names   []string `json:"names"`
	Install string   `json:"install"`
}

func ReadPackages(request []byte) (Packages, error) {
	var req struct {
		Spec struct {
			Packages

			Suites []struct {
				Name     string        `json:"name"`
				Packages *SpecPackages `json:"packages"`
			} `json:"suites"`
		} `json:"spec"`
	}

	if err := json.Unmarshal(request, &req); err != nil {
		return Packages{}, fmt.Errorf("request: %w", err)
	}

	out := req.Spec.Packages

	for _, s := range req.Spec.Suites {
		if s.Packages != nil {
			if out.Suites == nil {
				out.Suites = map[string]SpecPackages{}
			}

			out.Suites[s.Name] = *s.Packages
		}
	}

	return out, nil
}

// The images of the steps that fetch, build and push a cached image, pinned
// by digest. Neither runs the repository's code.
var (
	registryImageResource = M{"type": "registry-image", "source": M{"repository": "concourse/registry-image-resource", "tag": "1.19.0"},
		"version": M{"digest": "sha256:1905478041da9cc1c795c65a8cfa92df99b2c0dbc77919e7ee191cd9bc0e3d4b"}}
	ociBuildTask = M{"type": "registry-image", "source": M{"repository": "concourse/oci-build-task", "tag": "0.15.8"},
		"version": M{"digest": "sha256:f41d533fac399d8838722c9b6bc31352d4e28f5dc31c9f771d9a2d300d37fe7e"}}
)

const (
	packageCache = "pkgcache"
	envCache     = "PIPELINEGEN_PACKAGE_CACHE"
)

func short(key string) string {
	if len(key) > 12 {
		return key[:12]
	}

	return key
}

// cachedResource is the resource a cached image is pushed through, and the
// artifact its suites' tasks run in.
func cachedResource(key string) string { return "cached-" + short(key) }

func imageJob(key string) string { return "image-" + short(key) }

// cachedKey is the key of the cached image of a suite's steps that run in
// image, if they have one.
func (g *gen) cachedKey(suite, image string) string {
	sp, ok := g.pkgs.Suites[suite]
	if !ok || g.pkgs.Images == nil || image == "" {
		return ""
	}

	if k, ok := sp.Images[image]; ok {
		return k
	}

	// a suite whose image is the planner's took the planner's name
	if image == plannerImage {
		return sp.Images[g.plannerFrom[suite]]
	}

	return ""
}

func credentials(v string) M {
	if v == "" {
		return M{}
	}

	return M{"username": "((" + v + ".username))", "password": "((" + v + ".password))"}
}

// imageResources are the resources the image jobs push through. They are
// never checked: the jobs ask the registry themselves, and only a push uses
// them.
func (g *gen) imageResources() []any {
	if g.pkgs.Images == nil {
		return nil
	}

	var out []any

	for _, b := range g.pkgs.Images.Builds {
		src := M{"repository": g.pkgs.Images.Registry, "tag": b.Key}
		maps.Copy(src, credentials(g.pkgs.Images.Credentials))

		out = append(out, M{"name": cachedResource(b.Key), "type": "registry-image", "source": src, "check_every": "never"})
	}

	return out
}

// baseSource is the registry-image source of a cached image's base: the
// planner's, or the include's resource of that name.
func (g *gen) baseSource(base string) (M, error) {
	if base == plannerImage {
		return maps.Clone(g.ci.Planner.Image), nil
	}

	src, ok := g.included[base]
	if !ok {
		return nil, fmt.Errorf("%w: a cached image is built on %s, which %s declares as no registry-image resource", ErrInclude, base, g.ci.Include)
	}

	return maps.Clone(src), nil
}

// imageJobs build each cached image the registry hasn't got, when the config
// changes. They take nothing from the repository but the config's version:
// the recipe is the spec's and the base the pinned resource, so pushing
// rights never meet a change's code.
func (g *gen) imageJobs() ([]any, error) {
	if g.pkgs.Images == nil {
		return nil, nil
	}

	tags := g.place.tags(forPipeline, "", "")

	out := make([]any, 0, len(g.pkgs.Images.Builds))

	for _, b := range g.pkgs.Images.Builds {
		src := M{"repository": g.pkgs.Images.Registry, "tag": b.Key}
		maps.Copy(src, credentials(g.pkgs.Images.Credentials))

		source, err := json.Marshal(src)
		if err != nil {
			return nil, fmt.Errorf("image %s: %w", short(b.Key), err)
		}

		look := withTags(M{"task": "look", "config": M{
			"platform":       "linux",
			"image_resource": maps.Clone(registryImageResource),
			"outputs":        names("recipe", "gate"),
			"params":         M{"CACHED": string(source), "INSTALL": b.Install, "IMAGE": packagesRef(g.pkgs.Images.Registry, b.Key)},
			"run":            M{"path": "sh", "args": []any{"-ec", lookScript}},
		}}, tags)

		build := withTags(M{"task": "build", "privileged": true, "config": M{
			"platform":       "linux",
			"image_resource": maps.Clone(ociBuildTask),
			"inputs":         names("recipe", "base"),
			"outputs":        names("image"),
			"caches":         caches("cache"),
			"params":         M{"CONTEXT": "recipe", "IMAGE_ARG_base_image": "base/image.tar"},
			"run":            M{"path": "build"},
		}}, tags)

		push := withTags(M{"put": cachedResource(b.Key), "no_get": true, "params": M{"image": "image/image.tar"}}, tags)

		out = append(out, M{
			"name": imageJob(b.Key),
			"plan": []any{
				timeout(M{"in_parallel": []any{
					withTags(M{"get": "config", "trigger": true}, tags),
					withTags(M{"get": "base", "resource": b.Base, "params": M{"format": "oci"}}, tags),
				}}, g.stepMinutes()),
				limited(look, stepsKey, g.stepMinutes()),
				M{"load_var": "missing", "file": "gate/missing.json"},
				M{"across": []any{M{"var": "build", "values": "((.:missing))"}}, "do": []any{
					limited(build, stepsKey, g.stepMinutes()),
					timeout(push, g.stepMinutes()),
				}},
			},
		})
	}

	return out, nil
}

func packagesRef(registry, key string) string { return registry + ":" + key }

// lookScript asks the registry for the cached image and writes the recipe
// that builds it: a gate of one entry when it is missing, none when it is
// there. A registry that can't say counts as missing: a repository nothing
// was pushed to yet is refused rather than empty, and when the registry is
// down the push fails the job, which a person sees.
const lookScript = `if printf '{"source": %s}' "$CACHED" | /opt/resource/check > found.json && grep -q '"digest"' found.json; then
  echo "$IMAGE is in the registry: nothing to build"
  echo '[]' > gate/missing.json
else
  echo "$IMAGE isn't in the registry, or the registry can't say: building it"
  echo '["build"]' > gate/missing.json
fi
printf '%s' "$INSTALL" > recipe/install.sh
cat > recipe/Dockerfile <<'EOF'
ARG base_image
FROM ${base_image}
COPY install.sh /tmp/pipelinegen-install.sh
RUN PIPELINEGEN_PACKAGE_CLEAN=1 sh /tmp/pipelinegen-install.sh && rm -f /tmp/pipelinegen-install.sh
EOF
`

// fetchScript fetches a suite's cached image into the artifact its tasks run
// in, and the base at its digest when the registry hasn't got the image or
// can't be reached: the tasks then install what the base lacks.
const fetchScript = `digest=
if printf '{"source": %s}' "$CACHED" | /opt/resource/check > found.json; then
  digest=$(sed -n 's/.*"digest":"\(sha256:[0-9a-f]*\)".*/\1/p' found.json)
fi
if [ -n "$digest" ] && printf '{"source": %s, "version": {"digest": "%s"}}' "$CACHED" "$digest" | /opt/resource/in "$OUT" > /dev/null; then
  echo "the steps run in $IMAGE, with their packages"
  exit 0
fi
echo "$IMAGE isn't in the registry, or can't be pulled: the steps run in the base, and each installs the packages" >&2
rm -rf "${OUT:?}"/*
printf '{"source": %s, "version": {"digest": "%s"}}' "$BASE" "$BASE_DIGEST" | /opt/resource/in "$OUT" > /dev/null
`

// fetchImage is the task that makes the artifact a suite's steps that run in
// image run in, when that image has a cached one; nil when it hasn't.
func (g *gen) fetchImage(run, suite, image string) (M, string, error) {
	key := g.cachedKey(suite, image)
	if key == "" {
		return nil, "", nil
	}

	b, ok := g.build(key)
	if !ok {
		return nil, "", fmt.Errorf("%w: suite %s's cached image %s is no build of the spec's", ErrSteps, suite, short(key))
	}

	src := M{"repository": g.pkgs.Images.Registry, "tag": key}
	maps.Copy(src, credentials(g.pkgs.Images.PullCredentials))

	cached, err := json.Marshal(src)
	if err != nil {
		return nil, "", fmt.Errorf("image %s: %w", short(key), err)
	}

	base, err := g.baseSource(b.Base)
	if err != nil {
		return nil, "", err
	}

	baseJSON, err := json.Marshal(base)
	if err != nil {
		return nil, "", fmt.Errorf("image %s: %w", short(key), err)
	}

	artifact := cachedResource(key)

	return limited(withTags(M{"task": "image-" + short(key), "config": M{
		"platform":       "linux",
		"image_resource": maps.Clone(registryImageResource),
		"outputs":        names(artifact),
		"params": M{
			"CACHED": string(cached), "IMAGE": packagesRef(g.pkgs.Images.Registry, key), "OUT": artifact,
			"BASE": string(baseJSON), "BASE_DIGEST": b.Digest,
		},
		"run": M{"path": "sh", "args": []any{"-ec", fetchScript}},
	}}, g.suiteTags(run, suite)), stepsKey, g.stepMinutes()), artifact, nil
}

func (g *gen) build(key string) (SpecImageBuild, bool) {
	for _, b := range g.pkgs.Images.Builds {
		if b.Key == key {
			return b, true
		}
	}

	return SpecImageBuild{}, false
}

// readIncluded keeps the include's registry-image sources, the bases of
// cached images.
func (g *gen) readIncluded(file string) error {
	data, err := os.ReadFile(file) //nolint:gosec // the include the config names
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInclude, err)
	}

	var inc struct {
		Resources []M `yaml:"resources"`
	}

	if err := yaml.Unmarshal(data, &inc); err != nil {
		return fmt.Errorf("%w: %s: %w", ErrInclude, file, err)
	}

	g.included = map[string]M{}

	for _, r := range inc.Resources {
		if src, ok := r["source"].(M); ok && r["type"] == "registry-image" {
			g.included[fmt.Sprint(r["name"])] = src
		}
	}

	return nil
}

// withInstalls puts each task's install in front of its command, in every
// task that runs pipelinegen: the suite's packages before slice run and
// slice prepare, the planner's before the rest. The manager keeps its
// downloads in the task's cache, so each worker downloads a package once.
func (g *gen) withInstalls(p M) {
	jobs, _ := p["jobs"].([]any) //nolint:errcheck // the generator's own list

	suiteOf := map[string]string{}

	for _, r := range g.spec.Runs {
		for _, rs := range r.Suites {
			suiteOf[SuiteJob(r.Name, rs.Suite)] = rs.Suite
		}
	}

	for _, j := range jobs {
		job, ok := j.(M)
		if !ok {
			continue
		}

		name, _ := job["name"].(string) //nolint:errcheck // every job has one
		walkTasks(job, func(task M) { g.install(task, suiteOf[name]) })
	}
}

func (g *gen) install(task M, suite string) {
	cfg, ok := task["config"].(M)
	if !ok {
		return
	}

	run, _ := cfg["run"].(M)     //nolint:errcheck // a task without a run installs nothing
	p, _ := run["path"].(string) //nolint:errcheck // ditto

	args, _ := run["args"].([]any) //nolint:errcheck // a task without args may still run pipelinegen
	if args == nil {
		if s, ok := run["args"].([]string); ok {
			for _, a := range s {
				args = append(args, a)
			}
		}
	}

	var script string

	switch {
	case path.Base(p) == "pipelinegen":
		script = ""
	case p == "sh" && len(args) > 1 && strings.Contains(fmt.Sprint(args[1]), "tools/pipelinegen"):
		script = fmt.Sprint(args[1])
	default:
		return
	}

	install := ""
	if g.pkgs.Planner != nil {
		install = g.pkgs.Planner.Install
	}

	if path.Base(p) == "pipelinegen" && len(args) > 0 && args[0] == "slice" {
		install = g.pkgs.Suites[suite].Install
	}

	if install == "" {
		return
	}

	// a task that reports a broken build reports without them rather than not at all
	if task["task"] == "report-error" {
		install = "{\n" + install + "} || echo \"pipelinegen: the packages couldn't be installed; reporting without them\" >&2\n"
	}

	if path.Base(p) == "pipelinegen" {
		// sh -c's $0 and $@ are the tool and its arguments, run as they were
		run["path"], run["args"] = "sh", append([]any{"-ec", install + "exec \"$0\" \"$@\"\n", p}, args...)
	} else {
		run["args"] = append([]any{args[0], install + script}, args[2:]...)
	}

	cfg["caches"] = append(cacheList(cfg["caches"]), M{"path": packageCache})

	params, ok := cfg["params"].(M)
	if !ok {
		params = M{}
		cfg["params"] = params
	}

	params[envCache] = packageCache
}

func cacheList(v any) []M {
	switch x := v.(type) {
	case []M:
		return slices.Clone(x)
	case []any:
		out := make([]M, 0, len(x))
		for _, c := range x {
			if m, ok := c.(M); ok {
				out = append(out, m)
			}
		}

		return out
	}

	return nil
}

// walkTasks calls f with every task step with a config of its own under v,
// in hooks and nested steps too.
func walkTasks(v any, f func(M)) {
	switch x := v.(type) {
	case M:
		if _, ok := x["task"]; ok {
			if _, ok := x["config"].(M); ok {
				f(x)
			}
		}

		for _, k := range slices.Sorted(maps.Keys(x)) {
			walkTasks(x[k], f)
		}
	case []any:
		for _, e := range x {
			walkTasks(e, f)
		}
	case []M:
		for _, e := range x {
			walkTasks(e, f)
		}
	}
}

// taskImage is the artifact a suite's task that runs in image runs in: its
// cached image's when it has one.
func (g *gen) taskImage(suite, image string) string {
	if key := g.cachedKey(suite, image); key != "" {
		return cachedResource(key)
	}

	return image
}

// suiteImages are the tasks that fetch the cached images a suite's steps run
// in.
func (g *gen) suiteImages(run, suite string) ([]any, error) {
	s := g.suites[suite]
	images := []string{s.Image}

	if st := s.Steps; st != nil && st.Prepare && st.PrepareImage != "" {
		images = append(images, st.PrepareImage)
	}

	var out []any

	seen := map[string]bool{}

	for _, img := range images {
		task, artifact, err := g.fetchImage(run, suite, img)
		if err != nil {
			return nil, err
		}

		if task != nil && !seen[artifact] {
			seen[artifact] = true

			out = append(out, task)
		}
	}

	return out, nil
}
