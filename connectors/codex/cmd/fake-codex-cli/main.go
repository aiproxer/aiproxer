package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Deterministic Codex app-server JSON-RPC emulator for BuildBootstrap e2e.
// Spawns a long-lived grandchild so process-tree Kill can be proven.
func main() {
	pidFile := strings.TrimSpace(os.Getenv("FAKE_CODEX_PID_FILE"))
	childPIDFile := strings.TrimSpace(os.Getenv("FAKE_CODEX_CHILD_PID_FILE"))
	hang := strings.TrimSpace(os.Getenv("FAKE_CODEX_HANG")) == "1"
	for _, a := range os.Args[1:] {
		if after, ok := strings.CutPrefix(a, "--fake-pid-file="); ok {
			pidFile = after
		}
		if after, ok := strings.CutPrefix(a, "--fake-child-pid-file="); ok {
			childPIDFile = after
		}
		if a == "--fake-hang" {
			hang = true
		}
	}
	if pidFile == "" {
		pidFile = os.Args[0] + ".pid"
	}
	if childPIDFile == "" {
		childPIDFile = os.Args[0] + ".child.pid"
	}

	if pidFile != "" {
		_ = os.WriteFile(pidFile, []byte(strconv.Itoa(os.Getpid())), 0o600)
	}
	var child *exec.Cmd
	if childPIDFile != "" {
		if runtime.GOOS == "windows" {
			child = exec.Command("cmd", "/C", "ping -n 120 127.0.0.1 >nul")
		} else {
			child = exec.Command("sleep", "120")
		}
		if err := child.Start(); err == nil {
			_ = os.WriteFile(childPIDFile, []byte(strconv.Itoa(child.Process.Pid)), 0o600)
			go func() { _, _ = child.Process.Wait() }()
		}
	}

	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	out := bufio.NewWriter(os.Stdout)

	respond := func(id json.RawMessage, result any) {
		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
		_, _ = out.Write(append(b, '\n'))
		_ = out.Flush()
	}
	notify := func(method string, params any) {
		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
		_, _ = out.Write(append(b, '\n'))
		_ = out.Flush()
	}

	read := func() (method string, id json.RawMessage, ok bool) {
		if !in.Scan() {
			return "", nil, false
		}
		var parsed struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := json.Unmarshal(in.Bytes(), &parsed); err != nil {
			return "", nil, false
		}
		return parsed.Method, parsed.ID, true
	}

	method, id, ok := read()
	if !ok || method != "initialize" {
		os.Exit(1)
	}
	respond(id, map[string]any{"protocolVersion": 1})
	method, _, ok = read()
	if !ok || method != "initialized" {
		os.Exit(1)
	}
	method, id, ok = read()
	if !ok || method != "thread/start" {
		os.Exit(1)
	}
	respond(id, map[string]any{"id": "thread-fake-e2e"})
	method, id, ok = read()
	if !ok || method != "turn/start" {
		os.Exit(1)
	}
	notify("item/agentMessage/delta", map[string]any{"delta": "appserver-hi"})
	if hang {
		_, _ = io.Copy(io.Discard, os.Stdin)
		time.Sleep(2 * time.Minute)
		os.Exit(0)
	}
	notify("turn/completed", map[string]any{"turnId": "turn-fake-e2e"})
	respond(id, map[string]any{"turnId": "turn-fake-e2e"})
	_, _ = fmt.Fprintln(os.Stderr, "fake-codex-cli done")
	select {}
}
