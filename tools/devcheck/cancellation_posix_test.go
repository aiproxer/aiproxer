//go:build linux || darwin

package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/tools/devcheck/internal/evidence"
)

// The approved cancellation-safe verification contract requires descendants to
// stop and interrupted evidence to survive. Killing just the shell leaves its
// sleep child holding stdout open, so execution cannot complete until cleanup.
func TestContracts_CancellationStopsDescendantsAndKeepsEvidence(t *testing.T) {
	t.Parallel()
	for _, scenario := range []struct {
		name                string
		deadline, telemetry bool
	}{
		{name: "cancel"},
		{name: "deadline", deadline: true},
		{name: "test telemetry", telemetry: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			pidPath := filepath.Join(dir, "child.pid")
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			t.Cleanup(func() {
				data, err := os.ReadFile(pidPath)
				if err == nil {
					pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
					if pid > 0 {
						process, _ := os.FindProcess(pid)
						_ = process.Kill()
					}
				}
			})
			path := filepath.Join(dir, "manifest.json")
			recorder := newEvidenceRecorder(ctx, path, "contracts", evidence.Scope{Kind: "changed"}, dir)
			if scenario.deadline {
				var stop context.CancelFunc
				ctx, stop = context.WithTimeout(ctx, time.Second)
				defer stop()
			}
			ready := &cancelOnOutput{cancel: cancel, enabled: !scenario.deadline}
			script := `sleep 600 & echo $! > "$1"; printf 'ready\n'; wait`
			if scenario.telemetry {
				script = `sleep 600 & echo $! > "$1"; printf '%s\n' '{"Action":"pass","Package":"finished"}' '{"Action":"output","Package":"running","Output":"ready\n"}'; wait`
			}
			command := []string{"sh", "-c", script, "fixture", pidPath}
			done := make(chan error, 1)
			go func() {
				err := runContractCommandRecorded(ctx, dir, command, scenario.telemetry, ready, io.Discard, recorder)
				done <- errors.Join(err, recorder.finish(err))
			}()
			select {
			case err := <-done:
				want := context.Canceled
				if scenario.deadline {
					want = context.DeadlineExceeded
				}
				if !errors.Is(err, want) {
					t.Fatalf("interrupted command error = %v, want %v", err, want)
				}
			case <-time.After(5 * time.Second):
				// Clean up the known fixture child before joining our test worker.
				data, _ := os.ReadFile(pidPath)
				pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
				if pid > 0 {
					process, _ := os.FindProcess(pid)
					_ = process.Kill()
				}
				cancel()
				<-done
				t.Fatal("interruption left a descendant holding the output pipe open")
			}
			manifest := readManifest(t, path)
			if manifest.Outcome != evidence.OutcomeFailed || len(manifest.Steps) != 1 || manifest.Steps[0].Result != evidence.OutcomeFailed {
				t.Fatalf("interrupted evidence = %+v", manifest)
			}
			if scenario.telemetry && (manifest.Steps[0].Tests == nil || manifest.Steps[0].Tests.Passed != 1) {
				t.Fatalf("completed package counters lost: %+v", manifest.Steps[0])
			}
			log, err := os.ReadFile(manifest.Steps[0].LogPath)
			if err != nil || !strings.Contains(string(log), "ready") {
				t.Fatalf("interrupted output lost: %q, %v", log, err)
			}
		})
	}
}

// Observe the real CLI boundary: SIGINT must return a normal failure only after
// descendant cleanup and the deferred manifest write; repeat two must not run.
func TestCLI_InterruptWritesFailureAndStopsRepeats(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "manifest.json")
	for name, contents := range map[string]string{
		"go.mod": "module example.invalid/cancellation\n\ngo 1.26.9\n",
		"go":     "#!/bin/sh\n[ \"$1\" = build ] || exit 0\nif command -v flock >/dev/null; then exec 9>slot.lock; flock 9; fi\nprintf 'run\\n' >> runs\nsleep 600 & echo $! > child.pid\nprintf 'ready\\n'\nwait\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(contents), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "-test.run=^TestDevcheckSignalHelper$")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"), "DEVCHECK_CLI_FIXTURE="+manifestPath)
	cmd.WaitDelay = 3 * time.Second
	var signalErr error
	cmd.Stdout = &cancelOnOutput{enabled: true, cancel: func() { signalErr = cmd.Process.Signal(os.Interrupt) }}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		data, _ := os.ReadFile(filepath.Join(dir, "child.pid"))
		pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
		if pid > 0 {
			process, _ := os.FindProcess(pid)
			_ = process.Kill()
		}
	})
	if err := cmd.Wait(); err == nil || cmd.ProcessState.ExitCode() != 1 || signalErr != nil {
		t.Fatalf("interrupted CLI exit=%d err=%v signal=%v", cmd.ProcessState.ExitCode(), err, signalErr)
	}
	manifest := readManifest(t, manifestPath)
	if manifest.Outcome != evidence.OutcomeFailed || len(manifest.Steps) != 1 {
		t.Fatalf("interrupted CLI evidence=%+v", manifest)
	}
	runs, err := os.ReadFile(filepath.Join(dir, "runs"))
	if err != nil || string(runs) != "run\n" {
		t.Fatalf("continued after interruption: %q, %v", runs, err)
	}
	if flock, err := exec.LookPath("flock"); err == nil {
		if err := exec.CommandContext(t.Context(), flock, "-n", filepath.Join(dir, "slot.lock"), "true").Run(); err != nil {
			t.Fatalf("interruption retained the command's resource lock: %v", err)
		}
	}
}

func TestDevcheckSignalHelper(t *testing.T) {
	path := os.Getenv("DEVCHECK_CLI_FIXTURE")
	if path == "" {
		return
	}
	flag.CommandLine = flag.NewFlagSet("devcheck", flag.ExitOnError)
	task := os.Getenv("DEVCHECK_CLI_TASK")
	if task == "" {
		task = "build"
	}
	os.Args = []string{"devcheck", "-task=" + task, "-packages=./...", "-repeat=2", "-evidence=" + path}
	if timeout := os.Getenv("DEVCHECK_CLI_TIMEOUT"); timeout != "" {
		os.Args = append(os.Args, "-timeout="+timeout)
	}
	if output := os.Getenv("DEVCHECK_CLI_OUTPUT"); output != "" {
		os.Args = append(os.Args, "-output="+output)
	}
	main()
}

type cancelOnOutput struct {
	once    sync.Once
	cancel  context.CancelFunc
	enabled bool
}

func (w *cancelOnOutput) Write(data []byte) (int, error) {
	if w.enabled && strings.Contains(string(data), "ready") {
		w.once.Do(w.cancel)
	}
	return len(data), nil
}
