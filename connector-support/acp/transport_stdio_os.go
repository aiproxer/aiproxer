package acp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"
)

// osProcess implements Process using exec.Cmd with stdin/stdout pipes.
type osProcess struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	stderr io.ReadCloser
}

// OSProcessStarter implements ProcessStarter using os/exec.
type OSProcessStarter struct{}

// Run executes a short-lived child with exactly env and no stdin. Cancellation
// kills the same process tree as Start/Kill; Run reaps the child and joins the
// os/exec output pumps before returning. Nil output writers discard output.
// Remaining descendants are killed before the direct child is reaped, including
// on success, so its process-group identity cannot be recycled during cleanup.
// Run fails before spawning on platforms without this safe wait boundary.
func (OSProcessStarter) Run(ctx context.Context, cmdArgs []string, cwd string, env []string, stdout, stderr io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(cmdArgs) == 0 || cmdArgs[0] == "" || len(env) == 0 {
		return fmt.Errorf("acp: command and exact environment required")
	}
	cmd := exec.CommandContext(ctx, cmdArgs[0], cmdArgs[1:]...)
	cmd.Dir, cmd.Env = cwd, append([]string(nil), env...)
	if err := checkRunPlatform(); err != nil {
		return err
	}
	setProcessGroup(cmd)
	// Cancellation and completion share one tree shutdown, always before Wait
	// releases the PID. A late cancellation cannot signal a recycled group.
	cleanup := sync.OnceValue(func() error { return killProcessTree(cmd) })
	cmd.Cancel = cleanup
	cmd.WaitDelay = time.Second
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	waitErr := waitRunExit(cmd)
	cleanupErr := cleanup()
	err := cmd.Wait() // Reap and join the os/exec cancellation watcher/output pumps.
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return errors.Join(waitErr, cleanupErr, err)
}

// Start spawns a subprocess with stdin/stdout/stderr pipes. On Windows it sets
// CREATE_NEW_PROCESS_GROUP so taskkill /T can cleanly terminate the process tree.
func (OSProcessStarter) Start(cmdArgs []string, cwd string, env []string) (Process, error) {
	if len(cmdArgs) == 0 {
		return nil, fmt.Errorf("acp: empty command")
	}
	cmd := exec.Command(cmdArgs[0], cmdArgs[1:]...)
	if cwd != "" {
		cmd.Dir = cwd
	}
	if len(env) > 0 {
		cmd.Env = env
	}
	setProcessGroup(cmd)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("acp: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("acp: stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, fmt.Errorf("acp: stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stderr.Close()
		return nil, fmt.Errorf("acp: start process: %w", err)
	}
	return &osProcess{cmd: cmd, stdin: stdin, stdout: stdout, stderr: stderr}, nil
}

func (p *osProcess) PID() int              { return p.cmd.Process.Pid }
func (p *osProcess) Stdin() io.WriteCloser { return p.stdin }
func (p *osProcess) Stdout() io.ReadCloser { return p.stdout }
func (p *osProcess) Stderr() io.ReadCloser { return p.stderr }

func (p *osProcess) Wait() error {
	return p.cmd.Wait()
}

func (p *osProcess) Kill() error {
	return killProcessTree(p.cmd)
}
