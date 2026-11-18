package driver

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/sideways-works/pipelinegen/plugin"
)

func prepare(ctx context.Context, req plugin.DriverRequest, log io.Writer) error {
	set, err := settingsOf(req, plugin.CallPrepare)
	if err != nil {
		return err
	}

	ctx, cancel, err := withDeadline(ctx, req)
	if err != nil {
		return err
	}

	defer cancel()

	trimCaches(ctx, req, set, log)

	if set.limit > 0 {
		ctx, cancel = context.WithTimeout(ctx, set.limit)
		defer cancel()
	}

	args, err := argv(set, set.Script, filepath.Join(req.Work, "prepare"))
	if err != nil {
		return err
	}

	cmd := command(ctx, args, workDir(req.Tree, set), environment(req, set, nil), log)

	if err := runCmd(cmd, "the prepare "+kindOf(set), log); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("prepare: its time ran out: %w", err)
		}

		return fmt.Errorf("prepare: %w", err)
	}

	return nil
}

var exportLine = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*)=(.*)$`)

var errExport = errors.New("export")

// setup leaves what the script started running until the batch ends; then
// whatever still runs under the driver is stopped, in another session or
// process group too.
func setup(ctx context.Context, req plugin.DriverRequest, ready func(map[string]string) error, stop <-chan struct{}, log io.Writer) error {
	set, err := settingsOf(req, plugin.CallSetup)
	if err != nil {
		return err
	}

	defer sayLeftovers(log)

	dir := filepath.Join(req.Work, "setup")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("setup: %w", err)
	}

	export := filepath.Join(dir, "export")
	if err := os.WriteFile(export, nil, 0o600); err != nil {
		return fmt.Errorf("setup: %w", err)
	}

	trimCaches(ctx, req, set, log)

	if set.Script != "" || len(set.Command) > 0 {
		if err := setupScript(ctx, req, set, set.Script, dir, export, log); err != nil {
			return err
		}
	}

	exports, err := readExports(export)
	if err != nil {
		return err
	}

	if err := ready(exports); err != nil {
		return err
	}

	select {
	case <-stop:
	case <-ctx.Done():
		return nil
	}

	if set.Stop == "" {
		return nil
	}

	stopSet := set
	stopSet.Command = nil

	withExports := req
	withExports.Env = map[string]string{}
	maps.Copy(withExports.Env, req.Env)
	maps.Copy(withExports.Env, exports)

	if err := setupScript(ctx, withExports, stopSet, set.Stop, dir, export, log); err != nil {
		return fmt.Errorf("stop: %w", err)
	}

	return nil
}

func setupScript(ctx context.Context, req plugin.DriverRequest, set Settings, script, dir, export string, log io.Writer) error {
	if set.limit > 0 {
		var cancel context.CancelFunc

		ctx, cancel = context.WithTimeout(ctx, set.limit)
		defer cancel()
	}

	args, err := argv(set, script, dir)
	if err != nil {
		return err
	}

	cmd := exec.CommandContext(ctx, args[0], args[1:]...) //nolint:gosec // the suite's own command, from its trusted config
	cmd.Dir = workDir(req.Tree, set)
	cmd.Env = environment(req, set, map[string]string{EnvExport: export})
	// A file, not a pipe: a process the script leaves running keeps writing to
	// the log and holds nothing of this call open.
	cmd.Stdout, cmd.Stderr = log, log
	cmd.WaitDelay = stopWait

	what := "the set-up " + kindOf(set)
	if script == set.Stop && script != "" {
		what = "the set-up's stop script"
	}

	// The output bypasses the driver, so silence can't be detected.
	stopWatch := watch(log, what, nil)
	defer stopWatch()

	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("setup: its time ran out: %w", err)
		}

		return fmt.Errorf("setup: %w", err)
	}

	return nil
}

func readExports(path string) (map[string]string, error) {
	f, err := os.Open(path) //nolint:gosec // the set-up's own export file
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errExport, err)
	}

	defer func() { _ = f.Close() }() //nolint:errcheck // read only

	out := map[string]string{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)

	for n := 1; sc.Scan(); n++ {
		line := strings.TrimRight(sc.Text(), "\r")
		if line == "" {
			continue
		}

		m := exportLine.FindStringSubmatch(line)
		if m == nil {
			return nil, fmt.Errorf("%w: line %d of the export file is no NAME=value: %q", errExport, n, line)
		}

		out[m[1]] = m[2]
	}

	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("%w: %w", errExport, err)
	}

	return out, nil
}

func kindOf(set Settings) string {
	if set.Script == "" && len(set.Command) > 0 {
		return "command"
	}

	return "script"
}

func sayLeftovers(log io.Writer) {
	left := stopLeftovers()
	if len(left) == 0 {
		return
	}

	word := "processes"
	if len(left) == 1 {
		word = "process"
	}

	sayf(log, "shell-driver: the set-up left %d %s running when it ended: %s; stopped\n", len(left), word, strings.Join(left, ", "))
}
