module github.com/sideways-works/pipelinegen-driver-go

go 1.27.1

require github.com/sideways-works/pipelinegen v0.2.0

require (
	github.com/bazelbuild/buildtools v0.0.0-20260904073137-eaa4d125b423 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
)

tool github.com/sideways-works/pipelinegen/cmd/pipelinegen
