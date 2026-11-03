package driver

import (
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"

	"github.com/sideways-works/pipelinegen/plugin"
)

// The variables the driver gives a script beyond its environment.
const (
	EnvTree = "PIPELINEGEN_TREE"
	// EnvPrepared is the directory a prepare step fills and later steps read.
	EnvPrepared = "PIPELINEGEN_PREPARED"
	// EnvWork is the step's scratch directory for the batch.
	EnvWork = "PIPELINEGEN_WORK"
	// EnvCache is the step's directory kept between builds.
	EnvCache = "PIPELINEGEN_CACHE"
	// EnvExport is a set-up's export file: each NAME=value line reaches the
	// later steps as a variable.
	EnvExport = "PIPELINEGEN_EXPORT"
	// EnvSlices lists the slices a run's script runs, one per line;
	// EnvSlicesFile holds the same lines.
	EnvSlices     = "PIPELINEGEN_SLICES"
	EnvSlicesFile = "PIPELINEGEN_SLICES_FILE"
	// EnvResults is where a run's script may leave <name>.json in the tool's
	// result form, or <name>.status holding an exit status.
	EnvResults = "PIPELINEGEN_RESULTS"
	// EnvSlice and the ones after it describe the one slice a call runs.
	EnvSlice     = "PIPELINEGEN_SLICE"
	EnvSource    = "PIPELINEGEN_SOURCE"
	EnvTests     = "PIPELINEGEN_TESTS"
	EnvLabel     = "PIPELINEGEN_LABEL"
	EnvTarget    = "PIPELINEGEN_TARGET"
	EnvExtras    = "PIPELINEGEN_EXTRAS"
	EnvRecording = "PIPELINEGEN_RECORDING"
)

var kept = []string{
	"PIPELINEGEN_RUN", "PIPELINEGEN_SUITE", "PIPELINEGEN_BATCH",
	"PIPELINEGEN_RECORD", "PIPELINEGEN_RUN_FILE", "PIPELINEGEN_COMMIT", plugin.EnvMarks,
}

// drops keeps tokens out of a script even when the driver runs by hand.
func drops(name string) bool {
	upper := strings.ToUpper(name)
	if upper == "GITHUB_TOKEN" || upper == "GH_TOKEN" {
		return true
	}

	return strings.HasPrefix(upper, "PIPELINEGEN_") && !slices.Contains(kept, name)
}

// environment sets the driver's own variables over everything else.
func environment(req plugin.DriverRequest, set Settings, own map[string]string) []string {
	over := map[string]string{}
	maps.Copy(over, req.Env)
	maps.Copy(over, set.Env)
	maps.Copy(over, cacheVars(req, set))

	for k, v := range map[string]string{EnvTree: req.Tree, EnvPrepared: req.Prepared, EnvWork: req.Work, EnvCache: req.Cache} {
		if v != "" {
			over[k] = v
		}
	}

	maps.Copy(over, own)

	var env []string

	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if _, ok := over[name]; ok || drops(name) {
			continue
		}

		env = append(env, kv)
	}

	for _, k := range slices.Sorted(maps.Keys(over)) {
		env = append(env, k+"="+over[k])
	}

	return env
}

var driverVars = []string{
	EnvTree, EnvPrepared, EnvWork, EnvCache, EnvExport, EnvSlices, EnvSlicesFile, EnvResults,
	EnvSlice, EnvSource, EnvTests, EnvLabel, EnvTarget, EnvExtras, EnvRecording,
}

// envSettings refuses a variable set by both env and the caches, where one
// value would silently replace the other.
func envSettings(s Settings) error {
	by := map[string]string{}

	for _, k := range slices.Sorted(maps.Keys(s.Env)) {
		by[k] = fmt.Sprintf("env[%q]", k)
	}

	for i, c := range s.Caches {
		if c.Env == "" {
			continue
		}

		place := fmt.Sprintf("caches[%d].env", i)
		if other, ok := by[c.Env]; ok {
			return fmt.Errorf("%w: %s and %s both set %s", errSettings, other, place, c.Env)
		}

		by[c.Env] = place
	}

	for _, name := range slices.Sorted(maps.Keys(by)) {
		switch {
		case !envName.MatchString(name):
			return fmt.Errorf("%w: %s: %q is no variable's name", errSettings, by[name], name)
		case slices.Contains(driverVars, name):
			return fmt.Errorf("%w: %s: %s is the driver's own variable, which it sets itself", errSettings, by[name], name)
		}
	}

	return nil
}
