// Package commitmsg checks this repository's commit messages: one conventional subject line of at
// most 72 characters and nothing else, so the history reads as a list of what changed.
package commitmsg

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"unicode/utf8"
)

// MaxSubject is the longest subject line, in characters.
const MaxSubject = 72

// ErrMessage is a message that breaks the rule; the error says how.
var ErrMessage = errors.New("commit message")

var subject = regexp.MustCompile(`^(feat|fix|docs|test|build|ci|refactor|chore|perf)(\([a-z0-9][a-z0-9/._-]*\))?: \S`)

// Check checks a whole message as git hands it to a commit-msg hook: its comment lines (those
// starting with #) are not part of it.
func Check(msg string) error {
	var lines []string

	for line := range strings.SplitSeq(msg, "\n") {
		if strings.HasPrefix(line, "#") {
			continue
		}

		lines = append(lines, strings.TrimRight(line, "\r"))
	}

	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}

	switch {
	case len(lines) == 0:
		return fmt.Errorf("%w: empty", ErrMessage)
	case len(lines) > 1:
		return fmt.Errorf("%w: one subject line only, no body and no trailers: %q", ErrMessage, lines[0])
	}

	return CheckSubject(lines[0])
}

// CheckSubject checks one subject line.
func CheckSubject(s string) error {
	switch {
	case s != strings.TrimSpace(s):
		return fmt.Errorf("%w: space around the subject: %q", ErrMessage, s)
	case !subject.MatchString(s):
		return fmt.Errorf("%w: not type(scope): subject with a conventional type (feat, fix, docs, test, build, ci, refactor, chore, perf): %q", ErrMessage, s)
	case utf8.RuneCountInString(s) > MaxSubject:
		return fmt.Errorf("%w: %d characters, at most %d: %q", ErrMessage, utf8.RuneCountInString(s), MaxSubject, s)
	}

	return nil
}

// Commits checks every commit of revs in dir, merges left out, and returns one error naming each
// that breaks the rule.
func Commits(ctx context.Context, dir string, revs ...string) error {
	cmd := exec.CommandContext(ctx, "git", append([]string{"log", "--no-merges", "--format=%h%x00%B%x00"}, revs...)...) //nolint:gosec // git with this package's own arguments
	cmd.Dir = dir

	var stderr bytes.Buffer

	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("git log: %w: %s", err, strings.TrimSpace(stderr.String()))
	}

	var bad []error

	fields := strings.Split(string(out), "\x00")
	for i := 0; i+1 < len(fields); i += 2 {
		if err := Check(fields[i+1]); err != nil {
			bad = append(bad, fmt.Errorf("%s: %w", strings.TrimSpace(fields[i]), err))
		}
	}

	return errors.Join(bad...)
}
