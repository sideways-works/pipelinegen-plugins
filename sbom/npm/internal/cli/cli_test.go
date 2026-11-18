package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const fixture = "../../npm/testdata/workspace"

func run(t *testing.T, call, request string) (int, string, string) {
	t.Helper()

	var out, errOut bytes.Buffer

	code := Run([]string{call}, strings.NewReader(request), &out, &errOut)

	return code, out.String(), errOut.String()
}

func TestDescribe(t *testing.T) {
	code, out, _ := run(t, callDescribe, "")
	if code != exitOK {
		t.Fatalf("exit %d", code)
	}

	var d description
	if err := json.Unmarshal([]byte(out), &d); err != nil {
		t.Fatal(err)
	}

	if d.Kind != "sbom" || d.Namespaces[0] != "dep:npm" || d.Inputs[callUnits] != "per-set" {
		t.Errorf("describe = %+v", d)
	}
}

func TestUnits(t *testing.T) {
	code, out, stderr := run(t, callUnits, `{"protocol":1,"tree":"`+fixture+`","config":{}}`)
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, stderr)
	}

	a := oneSet(t, out)

	if len(a.Units) != 13 || len(a.Links) != 8 || strings.Join(a.Inputs, " ") != ".nvmrc package-lock.json package.json tools/lint/package-lock.json tools/lint/package.json" {
		t.Errorf("%d units, %d links, inputs %q", len(a.Units), len(a.Links), a.Inputs)
	}
}

func oneSet(t *testing.T, out string) unitSet {
	t.Helper()

	var a unitsAnswer
	if err := json.Unmarshal([]byte(out), &a); err != nil {
		t.Fatal(err)
	}

	if len(a.Sets) != 1 || a.Sets[0].Group != group {
		t.Fatalf("sets %+v, want the one set", a.Sets)
	}

	return a.Sets[0]
}

func TestUnitsForSomePaths(t *testing.T) {
	claimed := `"claimed":[{"path":"tools/lint/package-lock.json","role":"lockfile"},{"path":"package.json","role":"manifest"}]`

	for name, tc := range map[string]struct {
		paths string
		sets  int
	}{
		"a lockfile":           {`["tools/lint/package-lock.json"]`, 1},
		"a deleted lockfile":   {`["old/package-lock.json"]`, 1},
		"a manifest of no set": {`["package.json"]`, 0},
	} {
		t.Run(name, func(t *testing.T) {
			code, out, stderr := run(t, callUnits, `{"protocol":1,"tree":"`+fixture+`",`+claimed+`,"paths":`+tc.paths+`}`)
			if code != exitOK {
				t.Fatalf("exit %d: %s", code, stderr)
			}

			var a unitsAnswer
			if err := json.Unmarshal([]byte(out), &a); err != nil {
				t.Fatal(err)
			}

			if len(a.Sets) != tc.sets {
				t.Errorf("%d sets, want %d", len(a.Sets), tc.sets)
			}
		})
	}
}

func TestDocument(t *testing.T) {
	code, out, stderr := run(t, callDocument, `{"protocol":1,"tree":"`+fixture+`","config":{}}`)
	if code != exitOK || !strings.Contains(out, `"bomFormat":"CycloneDX"`) || !strings.Contains(out, "pkg:npm/react@18.3.1") {
		t.Errorf("exit %d: %.200s %s", code, out, stderr)
	}
}

func TestExitCodes(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package-lock.json"), []byte(`{"lockfileVersion":1}`), 0o600); err != nil {
		t.Fatal(err)
	}

	for name, c := range map[string]struct {
		call, req string
		want      int
	}{
		"version 1 lockfile":  {callUnits, `{"protocol":1,"tree":"` + dir + `"}`, exitCannotTell},
		"no lockfile":         {callUnits, `{"protocol":1,"tree":"` + t.TempDir() + `"}`, exitCannotTell},
		"no lockfile claimed": {callUnits, `{"protocol":1,"tree":"` + fixture + `","claimed":[{"path":"package.json","role":"manifest"}],"paths":["package.json"]}`, exitCannotTell},
		"wrong protocol":      {callUnits, `{"protocol":2}`, exitFailed},
		"bad json":            {callUnits, `{`, exitFailed},
		"unknown call":        {"links", `{"protocol":1}`, exitFailed},
	} {
		t.Run(name, func(t *testing.T) {
			if code, _, _ := run(t, c.call, c.req); code != c.want {
				t.Errorf("exit %d, want %d", code, c.want)
			}
		})
	}

	if code := Run(nil, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}); code != exitFailed {
		t.Errorf("no call: exit %d", code)
	}
}

func TestDetectCall(t *testing.T) {
	code, out, stderr := run(t, callDetect, `{"protocol":1,"tree":"`+fixture+`","paths":["package.json","package-lock.json","apps/web/README.md"]}`)
	if code != exitOK || strings.TrimSpace(out) != `{"claims":[{"path":"package.json","role":"manifest"},{"path":"package-lock.json","role":"lockfile"}]}` {
		t.Errorf("exit %d: %s %s", code, out, stderr)
	}
}

func TestUnitsForDetectedPaths(t *testing.T) {
	code, out, stderr := run(t, callUnits, `{"protocol":1,"tree":"`+fixture+`","paths":["package.json","package-lock.json",".nvmrc"]}`)
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, stderr)
	}

	a := oneSet(t, out)

	if strings.Join(a.Inputs, " ") != ".nvmrc package-lock.json package.json" {
		t.Errorf("inputs %q: only the lockfile pipelinegen sent", a.Inputs)
	}
}

func TestUnitsForClaimedFiles(t *testing.T) {
	req := `{"protocol":1,"tree":"` + fixture + `","claimed":[{"path":"tools/lint/package-lock.json","role":"lockfile"}]}`

	code, out, stderr := run(t, callUnits, req)
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, stderr)
	}

	if !strings.Contains(out, `"id":"dep:npm:left-pad"`) || strings.Contains(out, `"id":"dep:npm:react"`) {
		t.Errorf("units of another lockfile than the claimed one: %.300s", out)
	}

	if !strings.Contains(out, `"attributes":{"direct":true,"dev":true,"versions":["1.3.1"]}`) {
		t.Errorf("attributes: %.400s", out)
	}
}
