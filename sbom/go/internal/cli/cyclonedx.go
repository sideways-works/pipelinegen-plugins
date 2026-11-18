package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/sideways-works/pipelinegen-sbom-go/decls"
	"github.com/sideways-works/pipelinegen-sbom-go/deps"
	"github.com/sideways-works/pipelinegen-sbom-go/goid"
)

// The parts of a CycloneDX 1.6 document this plugin writes.
type (
	bom struct {
		BOMFormat    string       `json:"bomFormat"`
		SpecVersion  string       `json:"specVersion"`
		SerialNumber string       `json:"serialNumber"`
		Version      int          `json:"version"`
		Metadata     metadata     `json:"metadata"`
		Components   []component  `json:"components"`
		Dependencies []dependency `json:"dependencies"`
	}
	metadata struct {
		Tools struct {
			Components []component `json:"components"`
		} `json:"tools"`
	}
	component struct {
		Type       string     `json:"type"`
		BOMRef     string     `json:"bom-ref"`
		Name       string     `json:"name"`
		Version    string     `json:"version,omitempty"`
		PURL       string     `json:"purl,omitempty"`
		Scope      string     `json:"scope,omitempty"`
		Properties []property `json:"properties,omitempty"`
	}
	property struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	}
	dependency struct {
		Ref       string   `json:"ref"`
		DependsOn []string `json:"dependsOn"`
	}
)

// document answers the bill of materials. A module whose go.mod isn't in the
// module cache gets no dependencies entry, which CycloneDX reads as unknown.
func document(req Request, version string) (any, error) {
	tree, err := decls.Root(req.Tree)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errRequest, err)
	}

	mods, err := modules(tree, req.Config.Modules, req.Claimed)
	if err != nil {
		return nil, err
	}

	g, err := deps.Read(tree, mods, moduleCache(req.Config))
	if err != nil {
		return nil, fmt.Errorf("document: %w", err)
	}

	return cycloneDX(g, version), nil
}

func purl(p, v string) string {
	out := "pkg:golang/" + p
	if v != "" {
		out += "@" + v
	}

	return out
}

func cycloneDX(g *deps.Graph, version string) bom {
	b := bom{BOMFormat: "CycloneDX", SpecVersion: "1.6", Version: 1, Components: []component{}, Dependencies: []dependency{}}
	b.Metadata.Tools.Components = []component{{Type: "application", BOMRef: "tool:go-sbom", Name: "go-sbom", Version: version}}

	refs := map[string]string{}

	for _, m := range g.Modules {
		c := component{Type: "library", BOMRef: purl(m.Path, m.Version), Name: m.Path, Version: m.Version, PURL: purl(m.Path, m.Version)}
		if m.Sum != "" {
			// h1 is a hash over the module's file list, not over one
			// artifact, so it is a property, not a CycloneDX hash
			c.Properties = append(c.Properties, property{Name: "go:h1", Value: m.Sum})
		}

		if m.Replace != "" {
			c.Properties = append(c.Properties, property{Name: "go:replaced_by", Value: m.Replace})
		}

		if m.Indirect {
			c.Properties = append(c.Properties, property{Name: "go:indirect", Value: "true"})
		}

		refs[m.ID] = c.BOMRef
		b.Components = append(b.Components, c)
	}

	for _, t := range g.Tools {
		b.Components = append(b.Components, component{Type: "platform", BOMRef: t.ID, Name: t.ID[len(goid.DepNamespace)+1:], Version: t.Version})
	}

	for _, m := range g.Mains {
		ref := purl(m.Path, "")
		b.Components = append(b.Components, component{
			Type: "application", BOMRef: ref, Name: m.Path, PURL: ref,
			Properties: []property{{Name: "go:dir", Value: m.Dir}},
		})

		d := dependency{Ref: ref, DependsOn: []string{}}
		for _, id := range m.Requires {
			d.DependsOn = append(d.DependsOn, refs[id])
		}

		b.Dependencies = append(b.Dependencies, d)
	}

	unknown := map[string]bool{}
	for _, c := range g.CannotTell {
		unknown[c.ID] = true
	}

	on := map[string][]string{}
	for _, l := range g.Links {
		on[l.From] = append(on[l.From], refs[l.To])
	}

	for _, m := range g.Modules {
		if !unknown[m.ID] {
			b.Dependencies = append(b.Dependencies, dependency{Ref: refs[m.ID], DependsOn: append([]string{}, on[m.ID]...)})
		}
	}

	h := sha256.New()
	for _, c := range b.Components {
		h.Write([]byte(c.BOMRef + "\x00" + c.Version + "\x00"))

		for _, p := range c.Properties {
			h.Write([]byte(p.Name + "=" + p.Value + "\x00"))
		}
	}

	sum := hex.EncodeToString(h.Sum(nil))
	// The content's hash shaped as a version 5 UUID, so the same bill always gets the same serial.
	b.SerialNumber = "urn:uuid:" + sum[0:8] + "-" + sum[8:12] + "-5" + sum[13:16] + "-a" + sum[17:20] + "-" + sum[20:32]

	return b
}
