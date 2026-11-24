package generate

// The parts of piperun.yaml this runner writes, in piperun's own words (docs/reference/config.md in
// the piperun repository). yaml.v3 writes maps sorted and struct fields in order, so a spec always
// gives the same bytes.

type File struct {
	Version   int                 `yaml:"version"`
	Tools     map[string]Tool     `yaml:"tools,omitempty"`
	Images    map[string]Image    `yaml:"images,omitempty"`
	Caches    map[string]Cache    `yaml:"caches,omitempty"`
	Secrets   map[string]Secret   `yaml:"secrets,omitempty"`
	Resources map[string]Res      `yaml:"resources,omitempty"`
	Pipelines map[string]Pipeline `yaml:"pipelines"`
}

type Tool struct {
	Repository string  `yaml:"repository"`
	Release    Release `yaml:"release"`
	Asset      string  `yaml:"asset,omitempty"`
}

type Release struct {
	Version string `yaml:"version"`
	SHA256  string `yaml:"sha256,omitempty"`
	Sums    string `yaml:"sums,omitempty"`
}

type Cache struct {
	Shared *bool `yaml:"shared,omitempty"`
}

type Secret struct {
	From   string `yaml:"from"`
	Key    string `yaml:"key"`
	Writes bool   `yaml:"writes,omitempty"`
}

type Res struct {
	Uses string         `yaml:"uses"`
	With map[string]any `yaml:"with"`
}

type Pipeline struct {
	Triggers  []Trigger      `yaml:"triggers"`
	Jobs      map[string]Job `yaml:"jobs"`
	Templates map[string]Job `yaml:"templates,omitempty"`
}

type Trigger struct {
	Uses string         `yaml:"uses"`
	With map[string]any `yaml:"with,omitempty"`
}

type Job struct {
	After   []After          `yaml:"after,omitempty"`
	Needs   string           `yaml:"needs,omitempty"`
	Inputs  map[string]Input `yaml:"inputs,omitempty"`
	Steps   []Step           `yaml:"steps"`
	Sidecar *Step            `yaml:"sidecar,omitempty"`
	OnError []Step           `yaml:"on_error,omitempty"`
	Finally []Step           `yaml:"finally,omitempty"`
	Retry   *Retry           `yaml:"retry,omitempty"`
	Minutes int              `yaml:"minutes,omitempty"`
	Emits   []string         `yaml:"emits,omitempty"`
}

// After is a job to wait for: by name it waits for the job to pass, with Ended for its end however
// it ended.
type After struct {
	Job   string `yaml:"job"`
	Ended bool   `yaml:"ended,omitempty"`
}

type Input struct {
	Artifact string `yaml:"artifact,omitempty"`
}

type Step struct {
	Name    string            `yaml:"name,omitempty"`
	Uses    string            `yaml:"uses,omitempty"`
	Run     []string          `yaml:"run,omitempty"`
	Image   string            `yaml:"image,omitempty"`
	Tools   []string          `yaml:"tools,omitempty"`
	Env     map[string]string `yaml:"env,omitempty"`
	Secrets map[string]string `yaml:"secrets,omitempty"`
	// Caches mount caches under the file's caches, by name, each at its absolute path.
	Caches     map[string]string `yaml:"caches,omitempty"`
	Outputs    map[string]string `yaml:"outputs,omitempty"`
	Privileged bool              `yaml:"privileged,omitempty"`
	Trust      string            `yaml:"trust,omitempty"`
	Rerun      string            `yaml:"rerun,omitempty"`
}

type Retry struct {
	On       []string `yaml:"on"`
	Attempts int      `yaml:"attempts"`
}
