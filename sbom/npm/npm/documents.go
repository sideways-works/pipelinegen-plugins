package npm

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// The CycloneDX 1.6 shapes this package reads and writes.
type cdxBOM struct {
	BOMFormat    string          `json:"bomFormat"`   //nolint:tagliatelle // CycloneDX's field name
	SpecVersion  string          `json:"specVersion"` //nolint:tagliatelle // CycloneDX's field name
	Version      int             `json:"version"`
	Metadata     *cdxMetadata    `json:"metadata,omitempty"`
	Components   []cdxComponent  `json:"components"`
	Dependencies []cdxDependency `json:"dependencies"`
}

type cdxMetadata struct {
	Tools struct {
		Components []cdxTool `json:"components"`
	} `json:"tools"`
}

type cdxTool struct {
	Type    string `json:"type"`
	Name    string `json:"name"`
	Version string `json:"version"`
}

type cdxComponent struct {
	BOMRef  string    `json:"bom-ref"` //nolint:tagliatelle // CycloneDX's field name
	Type    string    `json:"type"`
	Group   string    `json:"group,omitempty"`
	Name    string    `json:"name"`
	Version string    `json:"version"`
	PURL    string    `json:"purl"`
	Hashes  []cdxHash `json:"hashes,omitempty"`
	Scope   string    `json:"scope,omitempty"`
}

type cdxHash struct {
	Alg     string `json:"alg"`
	Content string `json:"content"`
}

type cdxDependency struct {
	Ref       string   `json:"ref"`
	DependsOn []string `json:"dependsOn"` //nolint:tagliatelle // CycloneDX's field name
}

// The SPDX 2.3 JSON fields this package reads.
type spdxDocument struct {
	SPDXVersion string `json:"spdxVersion"` //nolint:tagliatelle // SPDX's field name
	Packages    []struct {
		Name         string `json:"name"`
		VersionInfo  string `json:"versionInfo"` //nolint:tagliatelle // SPDX's field name
		ExternalRefs []struct {
			ReferenceType    string `json:"referenceType"`    //nolint:tagliatelle // SPDX's field name
			ReferenceLocator string `json:"referenceLocator"` //nolint:tagliatelle // SPDX's field name
		} `json:"externalRefs"` //nolint:tagliatelle // SPDX's field name
		Checksums []struct {
			Algorithm     string `json:"algorithm"`
			ChecksumValue string `json:"checksumValue"` //nolint:tagliatelle // SPDX's field name
		} `json:"checksums"`
	} `json:"packages"`
}

// documents adds the npm packages of CycloneDX or SPDX documents that no
// lockfile lists, for a part of the tree without one (a vendored bundle).
func (b *builder) documents(tree string, files []string) error {
	for _, f := range files {
		data, err := os.ReadFile(filepath.Join(tree, filepath.FromSlash(f))) //nolint:gosec // a document the config names
		if err != nil {
			return fmt.Errorf("document: %w", err)
		}

		entries, err := readDocument(data)
		if err != nil {
			return fmt.Errorf("%w: %s: %w", ErrInvalid, f, err)
		}

		b.inputs = append(b.inputs, f)

		for _, e := range entries {
			id := Namespace + e.Name
			if _, listed := b.copies[id]; listed {
				continue
			}

			e.Path = f + "#" + e.Name + "@" + e.Version
			b.copies[id] = append(b.copies[id], e)
			b.owner[id] = f
			b.direct[id] = true
		}
	}

	return nil
}

func readDocument(data []byte) ([]*Entry, error) {
	var probe struct {
		BOMFormat   string `json:"bomFormat"`   //nolint:tagliatelle // CycloneDX's field name
		SPDXVersion string `json:"spdxVersion"` //nolint:tagliatelle // SPDX's field name
	}

	if err := json.Unmarshal(data, &probe); err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}

	switch {
	case probe.BOMFormat == "CycloneDX":
		return readCycloneDX(data)
	case probe.SPDXVersion != "":
		return readSPDX(data)
	default:
		return nil, fmt.Errorf("%w: neither CycloneDX nor SPDX JSON", ErrInvalid)
	}
}

func readCycloneDX(data []byte) ([]*Entry, error) {
	var b cdxBOM
	if err := json.Unmarshal(data, &b); err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}

	var out []*Entry

	for _, c := range b.Components {
		name, version, ok := npmPURL(c.PURL)
		if !ok {
			continue
		}

		e := &Entry{Name: name, Version: version}

		for _, h := range c.Hashes {
			e.Integrity += strings.ToLower(h.Alg) + "-" + h.Content + " "
		}

		e.Integrity = strings.TrimSpace(e.Integrity)
		out = append(out, e)
	}

	return out, nil
}

func readSPDX(data []byte) ([]*Entry, error) {
	var d spdxDocument
	if err := json.Unmarshal(data, &d); err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}

	var out []*Entry

	for _, p := range d.Packages {
		for _, r := range p.ExternalRefs {
			if r.ReferenceType != "purl" {
				continue
			}

			name, version, ok := npmPURL(r.ReferenceLocator)
			if !ok {
				continue
			}

			e := &Entry{Name: name, Version: version}
			for _, c := range p.Checksums {
				e.Integrity += strings.ToLower(c.Algorithm) + "-" + c.ChecksumValue + " "
			}

			e.Integrity = strings.TrimSpace(e.Integrity)
			out = append(out, e)
		}
	}

	return out, nil
}

// npmPURL reads a package URL of the npm type: pkg:npm/%40scope/name@1.0.0.
func npmPURL(purl string) (name, version string, ok bool) {
	rest, ok := strings.CutPrefix(purl, "pkg:npm/")
	if !ok {
		return "", "", false
	}

	rest, _, _ = strings.Cut(rest, "?")
	rest, _, _ = strings.Cut(rest, "#")

	at := strings.LastIndex(rest, "@")
	if at <= 0 {
		return "", "", false
	}

	name, err := url.PathUnescape(rest[:at])
	if err != nil {
		return "", "", false
	}

	return name, rest[at+1:], true
}

// PURL is the package URL of an npm package.
func PURL(name, version string) string {
	return "pkg:npm/" + strings.Replace(name, "@", "%40", 1) + "@" + version
}

// Document writes the model as a CycloneDX 1.6 bill of materials.
func (m *Model) Document(tool, version string) any {
	b := cdxBOM{BOMFormat: "CycloneDX", SpecVersion: "1.6", Version: 1, Metadata: &cdxMetadata{}, Components: []cdxComponent{}, Dependencies: []cdxDependency{}}
	b.Metadata.Tools.Components = []cdxTool{{Type: "application", Name: tool, Version: version}}

	refs := map[string][]string{}

	for _, u := range m.Units {
		for _, e := range m.Copies[u.ID] {
			name := e.Name
			if name == "" {
				name = strings.TrimPrefix(u.ID, Namespace)
			}

			c := cdxComponent{BOMRef: PURL(name, e.Version), Type: "library", Name: name, Version: e.Version, PURL: PURL(name, e.Version)}
			if group, n, ok := strings.Cut(name, "/"); ok && strings.HasPrefix(group, "@") {
				c.Group, c.Name = group, n
			}

			if e.Dev {
				c.Scope = "optional"
			}

			c.Hashes = hashes(e.Integrity)

			if !slices.ContainsFunc(b.Components, func(x cdxComponent) bool { return x.BOMRef == c.BOMRef }) {
				b.Components = append(b.Components, c)
			}

			refs[u.ID] = append(refs[u.ID], c.BOMRef)
		}
	}

	deps := map[string][]string{}
	for _, l := range m.Links {
		deps[l.From] = append(deps[l.From], refs[l.To]...)
	}

	for _, u := range m.Units {
		for _, r := range refs[u.ID] {
			on := slices.Clone(deps[u.ID])
			slices.Sort(on)
			b.Dependencies = append(b.Dependencies, cdxDependency{Ref: r, DependsOn: append([]string{}, slices.Compact(on)...)})
		}
	}

	return b
}

// hashes turns an integrity string ("sha512-<base64> sha1-<base64>") into
// CycloneDX hashes, hex encoded.
func hashes(integrity string) []cdxHash {
	names := map[string]string{"sha1": "SHA-1", "sha256": "SHA-256", "sha384": "SHA-384", "sha512": "SHA-512"}

	var out []cdxHash

	for part := range strings.FieldsSeq(integrity) {
		alg, b64, ok := strings.Cut(part, "-")
		if !ok || names[alg] == "" {
			continue
		}

		raw, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			continue
		}

		out = append(out, cdxHash{Alg: names[alg], Content: hex.EncodeToString(raw)})
	}

	return out
}
