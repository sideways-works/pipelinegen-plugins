// Package junit reads the test cases of a test.xml Bazel kept for a target.
package junit

import (
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"strings"
)

// Case is one test case.
type Case struct {
	Name string
	// Failed is set for a failure or an error; Message is what it said.
	Failed  bool
	Skipped bool
	Message string
}

type suites struct {
	Suites []suite `xml:"testsuite"`
	// A file with one suite at the top.
	Cases []testcase `xml:"testcase"`
}

type suite struct {
	Suites []suite    `xml:"testsuite"`
	Cases  []testcase `xml:"testcase"`
}

type testcase struct {
	Name      string   `xml:"name,attr"`
	ClassName string   `xml:"classname,attr"`
	Failures  []detail `xml:"failure"`
	Errors    []detail `xml:"error"`
	Skipped   *detail  `xml:"skipped"`
}

type detail struct {
	Message string `xml:"message,attr"`
	Text    string `xml:",chardata"`
}

// ErrRead wraps a test.xml that can't be read.
var ErrRead = errors.New("test.xml")

// ReadFile reads the cases of a test.xml; none for an empty one.
func ReadFile(path string) ([]Case, error) {
	data, err := os.ReadFile(path) //nolint:gosec // a file the test wrote
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrRead, err)
	}

	return Read(data)
}

// Read reads the cases of a test.xml's content.
func Read(data []byte) ([]Case, error) {
	if strings.TrimSpace(string(data)) == "" {
		return nil, nil
	}

	var top suites
	if err := xml.Unmarshal(data, &top); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrRead, err)
	}

	var out []Case

	add := func(cs []testcase) {
		for _, c := range cs {
			k := Case{Name: c.Name, Skipped: c.Skipped != nil}

			for _, d := range append(c.Failures, c.Errors...) {
				k.Failed = true
				k.Message = strings.TrimSpace(strings.Join([]string{k.Message, d.Message, d.Text}, "\n"))
			}

			out = append(out, k)
		}
	}

	var walk func([]suite)

	walk = func(ss []suite) {
		for _, s := range ss {
			add(s.Cases)
			walk(s.Suites)
		}
	}

	add(top.Cases)
	walk(top.Suites)

	return out, nil
}
