package generate

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
)

// ErrSettings is a runner config this runner can't write a pipeline from.
var ErrSettings = errors.New("piperun-runner config")

// Settings are the runner's config block, its plugin entry's config: what piperun.yaml needs that
// pipelinegen.yaml doesn't say.
type Settings struct {
	// Resource is the repository's git resource, which the triggers watch and `uses: git` checks out.
	Resource Resource `json:"resource"`
	// Images are the images steps run in, by the name a suite's image (or prepare image) and the
	// planner give: each a reference and its digest.
	Images map[string]Image `json:"images"`
	// Planner is where planning runs.
	Planner Planner `json:"planner"`
	// Secrets says where the tokens come from, by piperun's backend (sealed, env or file); each
	// token's key is its var's name in pipelinegen.yaml.
	Secrets Secrets `json:"secrets"`
	// Needs are piperun's `needs` rules, by suite; "default" for every suite without one.
	Needs map[string]string `json:"needs"`
	// Caches are the caches a suite's steps mount, by suite: the absolute path to mount at, by
	// the cache's name.
	Caches map[string]map[string]string `json:"caches"`
	// Pipelinegen is the release piperun mounts into pipelinegen's steps; a config with a
	// concourse section may leave it to that section's bootstrap pin.
	Pipelinegen *ToolPin `json:"pipelinegen"`
}

// ToolPin is a released tool: its repository (pipelinegen's by default), version and one of the
// binary's sha256 or the release's SHA256SUMS'.
type ToolPin struct {
	Repository string `json:"repository"`
	Version    string `json:"version"`
	SHA256     string `json:"sha256"`
	Sums       string `json:"sums"`
}

type Resource struct {
	Name   string `json:"name"`
	Remote string `json:"remote"`
	// Credential is the secret piperun clones with, a token for reading; none for a public
	// repository.
	Credential string `json:"credential"`
}

type Image struct {
	Ref    string `json:"ref"`
	Digest string `json:"digest"`
}

type Planner struct {
	// Image names one of Images.
	Image string `json:"image"`
	Needs string `json:"needs"`
}

type Secrets struct {
	From string `json:"from"`
}

var (
	name   = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)
	digest = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	// backends are piperun's secret backends.
	backends = []string{"sealed", "env", "file"}
)

// ReadSettings reads the config block strictly: a key it doesn't know is an error, since a setting
// it ignored would leave the pipeline other than meant.
func ReadSettings(config map[string]any) (Settings, error) {
	var s Settings

	data, err := json.Marshal(config)
	if err != nil {
		return s, fmt.Errorf("%w: %w", ErrSettings, err)
	}

	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()

	if err := dec.Decode(&s); err != nil {
		return s, fmt.Errorf("%w: %w", ErrSettings, err)
	}

	if s.Resource.Name == "" {
		s.Resource.Name = "source"
	}

	if s.Secrets.From == "" {
		s.Secrets.From = "sealed"
	}

	return s, s.check()
}

func (s Settings) check() error {
	var errs []error

	bad := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf("%w: %s", ErrSettings, fmt.Sprintf(format, args...)))
	}

	if !name.MatchString(s.Resource.Name) {
		bad("resource.name %q is no piperun name", s.Resource.Name)
	}

	if s.Resource.Remote == "" {
		bad("resource.remote: the repository's clone URL is required")
	}

	if s.Resource.Credential != "" && !name.MatchString(s.Resource.Credential) {
		bad("resource.credential %q is no piperun name", s.Resource.Credential)
	}

	for _, n := range sortedKeys(s.Images) {
		im := s.Images[n]
		if !name.MatchString(n) || im.Ref == "" || !digest.MatchString(im.Digest) {
			bad("images.%s needs a piperun name, a ref and a sha256: digest", n)
		}
	}

	if _, ok := s.Images[s.Planner.Image]; !ok {
		bad("planner.image %q names none of images", s.Planner.Image)
	}

	if !slices.Contains(backends, s.Secrets.From) {
		bad("secrets.from %q: sealed, env or file", s.Secrets.From)
	}

	return errors.Join(errs...)
}

// needs is the suite's needs rule, else the default one.
func (s Settings) needs(suite string) string {
	if n, ok := s.Needs[suite]; ok {
		return n
	}

	return s.Needs["default"]
}
