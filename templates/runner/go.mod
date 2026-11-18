module github.com/sideways-works/pipelinegen-plugins/templates/runner

go 1.27.1

require (
	github.com/sideways-works/pipelinegen v0.3.1
	go.yaml.in/yaml/v3 v3.0.5
)

require github.com/bazelbuild/buildtools v0.0.0-20260904073137-eaa4d125b423 // indirect

tool github.com/sideways-works/pipelinegen/cmd/pipelinegen
