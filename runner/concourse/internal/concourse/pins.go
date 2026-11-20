package concourse

import (
	"errors"
	"fmt"

	"github.com/sideways-works/pipelinegen/plugin"
)

// ErrPin is a plugin pinned in a way the pipeline's tasks can't fetch.
var ErrPin = errors.New("plugin pin")

// minManifests is the first pipelinegen release that reads a plugin pinned by a zip, and builds one
// pinned by a commit from the manifest.yaml in its directory.
const minManifests = "v0.5.0"

// checkPins refuses the pins the tasks can't fetch. With pipelinegen pinned by a release before
// minManifests nothing changes: a commit is built with go build, and a zip is no pin yet. From it on, a
// zip is fetched and unpacked as pipelinegen does, and a commit is refused: pipelinegen builds it from
// its manifest in a container, which a task could run only privileged with a container runtime of its
// own, so a pull request's code would run as root on the worker. The runner is fetched by the
// bootstrap before anything that unpacks a zip runs, so it takes a release.
func checkPins(spec plugin.Spec) error {
	pg := tool(spec, plugin.ToolPipelinegen)
	manifests := pg.Commit != "" || atLeast(pg.Version, minManifests)
	built := pg.Commit == "" && manifests
	runner := ownRunner(spec).Name

	for _, t := range plugins(spec) {
		switch {
		case t.Zip != nil && t.Name == runner:
			return fmt.Errorf("%w: plugins.%s: the bootstrap fetches the runner before anything that unpacks a zip runs: pin the runner by its release",
				ErrPin, t.Name)
		case t.Zip != nil && !manifests:
			return fmt.Errorf("%w: plugins.%s is pinned by a zip, which pipelinegen reads from %s on, and the pipeline pins pipelinegen %s",
				ErrPin, t.Name, minManifests, pg.Version)
		case t.Commit != "" && built:
			return fmt.Errorf("%w: plugins.%s is pinned by commit %s, which pipelinegen %s builds from its manifest.yaml in a container, "+
				"and a Concourse task has none to build in: build it into a zip (pipelinegen plugin build <dir> --zip <out.zip>), "+
				"host it, and pin that (pipelinegen plugin add --zip <url> --sha256 <sha256>)", ErrPin, t.Name, t.Commit, pg.Version)
		}
	}

	return nil
}
