// Command commit-msg is this repository's commit-msg hook (bazel run //tools:hooks installs it):
// it refuses a message that is not one conventional subject line.
package main

import (
	"fmt"
	"os"

	"github.com/sideways-works/pipelinegen-plugins/tools/commitmsg"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: commit-msg <message file>")
		os.Exit(2)
	}

	msg, err := os.ReadFile(os.Args[1]) //nolint:gosec // the message file git hands the hook
	if err == nil {
		err = commitmsg.Check(string(msg))
	}

	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
