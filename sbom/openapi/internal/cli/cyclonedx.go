package cli

import (
	"strings"

	"github.com/sideways-works/pipelinegen-sbom-openapi/openapi"
)

// The CycloneDX 1.6 shapes this plugin writes: a service per document, with a
// nested service per operation.
type bom struct {
	BOMFormat   string    `json:"bomFormat"`   //nolint:tagliatelle // CycloneDX's field name
	SpecVersion string    `json:"specVersion"` //nolint:tagliatelle // CycloneDX's field name
	Version     int       `json:"version"`
	Metadata    metadata  `json:"metadata"`
	Services    []service `json:"services"`
}

type metadata struct {
	Tools struct {
		Components []tool `json:"components"`
	} `json:"tools"`
}

type tool struct {
	Type    string `json:"type"`
	Name    string `json:"name"`
	Version string `json:"version"`
}

type service struct {
	BOMRef     string     `json:"bom-ref"` //nolint:tagliatelle // CycloneDX's field name
	Name       string     `json:"name"`
	Endpoints  []string   `json:"endpoints,omitempty"`
	Properties []property `json:"properties,omitempty"`
	Services   []service  `json:"services,omitempty"`
}

type property struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

const digestProperty = "pipelinegen:digest"

// cycloneDX lists the documents read, each with its operations; with only
// set, just those files.
func cycloneDX(m *openapi.Model, version string, only map[string]bool) bom {
	b := bom{BOMFormat: "CycloneDX", SpecVersion: "1.6", Version: 1, Services: []service{}}
	b.Metadata.Tools.Components = []tool{{Type: "application", Name: "openapi-sbom", Version: version}}

	digest := map[string]string{}
	for _, u := range m.Units {
		digest[u.ID] = u.Digest
	}

	files := map[string]bool{}

	for _, u := range m.Units {
		if u.Kind != openapi.KindDocument || files[u.File] || (only != nil && !only[u.File]) {
			continue
		}

		files[u.File] = true

		doc := service{
			BOMRef: u.ID, Name: u.File, Endpoints: m.Servers[u.File],
			Properties: []property{{Name: digestProperty, Value: u.Digest}},
		}

		for _, op := range m.OperationsOf(u.File) {
			s := service{
				BOMRef: op.ID, Name: op.OperationID,
				Properties: []property{{Name: digestProperty, Value: digest[op.ID]}},
			}

			if !strings.HasPrefix(op.Path, "webhook:") {
				for _, base := range m.Servers[u.File] {
					s.Endpoints = append(s.Endpoints, strings.TrimSuffix(base, "/")+op.Path)
				}

				s.Properties = append(s.Properties, property{Name: "pipelinegen:route", Value: op.Method + " " + op.Path})
			}

			doc.Services = append(doc.Services, s)
		}

		b.Services = append(b.Services, doc)
	}

	return b
}
