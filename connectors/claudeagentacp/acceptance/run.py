#!/usr/bin/env python3
"""Issue #1314 spawned-stack acceptance. Run via make claude-turn-acceptance.

Deployment certification and its external login probe are substituted by a Go
build overlay; a Process.Wait decorator supplies the delayed-join gate. The
packaged command, supervisor, RPC, host, frontend, canonical conversion,
auth-root ownership and runtime pool are real.
"""
import argparse
from concurrent.futures import ThreadPoolExecutor
import hashlib
import http.client
import json
import os
from pathlib import Path
import shutil
import socket
import subprocess
import sys
import tempfile
import threading
import time
import urllib.error
import urllib.parse
import urllib.request


ROOT = Path(__file__).resolve().parents[3]


def command(args, env, log):
    with log.open("wb") as output:
        subprocess.run(args, cwd=ROOT, env=env, stdout=output, stderr=subprocess.STDOUT,
                       check=True, timeout=900)


def overlay(scratch):
    source = ROOT / "connectors/claudeagentacp/internal/service/engine.go"
    text = source.read_text()
    original = "return engineDeps{host: os.Environ(), starter: acp.OSProcessStarter{}, log: slog.Default(), certify: certifyDeployment}"
    fake = scratch / "fake_acp.py"
    # Paths are baked into this test binary, not accepted from production config.
    replacement = '''deps := engineDeps{host: os.Environ(), starter: acceptanceStarter{}, log: slog.Default()}
    deps.identity.Probe = func(ctx context.Context, _, _ string, _ []string) ([]byte, error) {
        return []byte(`{"loggedIn":true,"apiProvider":"firstParty","authMethod":"claude.ai","subscriptionType":"max"}`), ctx.Err()
    }
    deps.certify = func(ctx context.Context, r product.AdapterResolver, cfg Config, host, env []string, cwd string) (product.RuntimeProfile, product.IdentityExtension, error) {
        _, err := r.Policy.Check(ctx, environmentValue(env, "CLAUDE_CONFIG_DIR"), cfg.AdminPolicy)
        executable, execErr := os.Executable()
        return product.RuntimeProfile{Executable: FAKE_ADAPTER, CLIExecutable: FAKE_ADAPTER}, product.IdentityExtension{
            SupervisorExecutable: executable, NodeExecutable: PYTHON, Bootstrap: FAKE_SCRIPT,
            AdapterRoot: FAKE_ROOT, BundleSHA256: "test-only-fake-acp"}, errors.Join(ctx.Err(), err, execErr)
    }
    return deps'''
    for key, value in {"FAKE_ADAPTER": str(scratch / "dist/index.js"), "PYTHON": sys.executable,
                       "FAKE_SCRIPT": str(fake), "FAKE_ROOT": str(scratch)}.items():
        replacement = replacement.replace(key, json.dumps(value))
    assert text.count(original) == 1, "certification construction seam changed; review overlay"
    decoration = '''
// Test-only OS-boundary decorator. Never included in a production build.
type acceptanceStarter struct{}
func (acceptanceStarter) Start(cmd []string, cwd string, env []string) (acp.Process, error) {
    p, err := (acp.OSProcessStarter{}).Start(cmd, cwd, env)
    if err != nil { return nil, err }
    return &acceptanceProcess{Process:p}, nil
}
type acceptanceProcess struct { acp.Process }
func (p *acceptanceProcess) Wait() error {
    err := p.Process.Wait()
    pid, _ := json.Marshal(p.PID())
    if _, statErr := os.Stat(FAKE_ROOT + "/" + string(pid) + ".hold"); statErr == nil {
        _ = os.WriteFile(FAKE_ROOT + "/retirement-wait", []byte("waiting"), 0600)
        deadline := time.Now().Add(15*time.Second)
        for {
            if _, e := os.Stat(FAKE_ROOT + "/release-retirement"); e == nil { break }
            if time.Now().After(deadline) { return errors.Join(err, acp.ErrCleanupIncomplete) }
            time.Sleep(10*time.Millisecond)
        }
        _ = os.WriteFile(FAKE_ROOT + "/retirement-released", []byte("joined"), 0600)
    }
    return err
}
'''.replace("FAKE_ROOT", json.dumps(str(scratch)))
    modified = scratch / "engine.go"
    modified.write_text(text.replace(original, replacement) + decoration)
    path = scratch / "overlay.json"
    path.write_text(json.dumps({"Replace": {str(source): str(modified)}}))
    shutil.copyfile(Path(__file__).with_name("fake_acp.py"), fake)
    return path


def request(base, path, payload=None, submissions=None):
    data = None if payload is None else json.dumps(payload).encode()
    if submissions is not None:
        connection = http.client.HTTPConnection(urllib.parse.urlsplit(base).netloc, timeout=60)
        try:
            connection.request("POST", path, data, {"Content-Type": "application/json"})
            # Both client requests are submitted before either response is
            # consumed. The production host intentionally serializes plugin
            # Execute calls per instance; do not require parallel ACP prompts.
            submissions.wait(timeout=20)
            response = connection.getresponse()
            body = json.load(response)
            assert response.status == 200, body
            return response.status, body
        finally:
            connection.close()
    req = urllib.request.Request(base + path, data=data, headers={"Content-Type": "application/json"})
    try:
        with urllib.request.urlopen(req, timeout=60) as response:
            return response.status, json.load(response)
    except urllib.error.HTTPError as error:
        raise AssertionError(f"HTTP {error.code}: {error.read().decode()}") from error


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--scratch", type=Path, help="new external evidence directory (retained)")
    args = parser.parse_args()
    if args.scratch:
        scratch = args.scratch.resolve()
        scratch.mkdir(mode=0o700, parents=True, exist_ok=False)
    else:
        scratch = Path(tempfile.mkdtemp(prefix="claude-turn-", dir=os.environ.get("TMPDIR")))
    assert not scratch.is_relative_to(ROOT), "scratch must be outside the repository"
    print("Evidence:", scratch, flush=True)
    env = os.environ.copy()
    env["GOFLAGS"] = (env.get("GOFLAGS", "") + " -overlay=" + str(overlay(scratch))).strip()
    go_command = os.environ.get("GO", "go")
    command([go_command, "run", "./tools/backendplugin/package_plugins", "-root", str(ROOT), "-profile", "full",
             "-select", "claudeagentacp", "-dest", str(scratch / "plugins")], env, scratch / "package.log")
    # The host is an ordinary, unmodified production build.
    host_env = os.environ.copy()
    command([go_command, "build", "-o", str(scratch / "aipstd"), "./cmd/aipstd"], host_env, scratch / "host-build.log")
    env = host_env.copy()
    home = scratch / "home"
    home.mkdir()
    (home / ".claude").mkdir()
    env["HOME"] = str(home)
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        port = sock.getsockname()[1]
    base = "http://127.0.0.1:" + str(port)
    config = scratch / "config.yaml"
    config.write_text('''server: {address: "127.0.0.1:PORT"}
continuity: {in_memory: true, store: memory}
access: {mode: single_user}
logging: {level: debug, format: text}
plugins:
  backend_discovery:
    enabled: true
    paths: [PLUGIN]
    instances: {claude-acp: {kind: claudeagentacp, execution_env: [HOME, PATH, TMPDIR]}}
  frontends: [{id: openai-legacy, enabled: true, config: {}}]
  backends:
    - id: claude-acp
      kind: claudeagentacp
      enabled: true
      config:
        executable: EXECUTABLE
        default_workspace: WORKSPACE
        credential_source: host_login
        admin_policy: honor
        permission_policy: bypass
        max_runtimes: 4
'''.replace("PORT", str(port)).replace("PLUGIN", json.dumps(str(scratch / "plugins/claudeagentacp")))
        .replace("EXECUTABLE", json.dumps(str(scratch / "dist/index.js")))
        .replace("WORKSPACE", json.dumps(str(scratch))))
    receipts = []
    with (scratch / "host.log").open("wb") as log:
        host = subprocess.Popen([str(scratch / "aipstd"), "--config", str(config), "serve"],
                                cwd=scratch, env=env, stdout=log, stderr=subprocess.STDOUT)
        try:
            deadline = time.monotonic() + 60
            while True:
                assert host.poll() is None, "host exited; see host.log"
                try:
                    status, models = request(base, "/v1/models")
                    if status == 200 and any(item["id"] == "claude-acp:anthropic/claude-sonnet-5.5" for item in models["data"]):
                        break
                except (OSError, urllib.error.URLError):
                    pass
                assert time.monotonic() < deadline, "model discovery timed out; see host.log"
                # Readiness retry, not a lifecycle-ordering assertion.
                time.sleep(0.1)

            def turn(number, second=False, submissions=None):
                messages = [{"role": "user", "content": f"Remember the number {number}. Reply with just: ok"}]
                if second:
                    messages += [{"role": "assistant", "content": "ok"}, {"role": "user", "content":
                        "What number did I ask you to remember? Reply with just the number."}]
                payload = {"model": "claude-acp:anthropic/claude-sonnet-5.5", "stream": False, "messages": messages}
                status, response = request(base, "/v1/chat/completions", payload, submissions)
                assert status == 200 and response["choices"][0]["message"]["content"] == (str(number) if second else "ok"), response
                return {"request": payload, "status": status, "response": response}

            receipts.append(turn(41))
            receipts.append(turn(41, True))
            receipts.append(turn(41, True))  # Byte-identical payload, no client session hint.
            with ThreadPoolExecutor(max_workers=2) as workers:
                for second in (False, True, True):
                    submissions = threading.Barrier(2)
                    receipts.extend(workers.map(lambda number: turn(number, second, submissions), (42, 43)))
            # Fresh work after repeated requests proves the instance is not poisoned.
            receipts.append(turn(44))
            receipts.append(turn(44, True))

            # A transport cancellation starts physical retirement. The external
            # test-only Wait decorator holds its join while unrelated work is served.
            connection = http.client.HTTPConnection("127.0.0.1", port, timeout=20)
            payload = {"model": "claude-acp:anthropic/claude-sonnet-5.5", "stream": True,
                       "messages": [{"role": "user", "content": "Remember the number 45. DELAY_RETIREMENT"}]}
            connection.request("POST", "/v1/chat/completions", json.dumps(payload), {"Content-Type": "application/json"})
            response = connection.getresponse()
            assert response.status == 200
            while True:
                line = response.readline()
                assert line, "stream ended before content"
                if line.startswith(b"data:") and b'"ok"' in line:
                    break
            response.close()
            connection.close()
            deadline = time.monotonic() + 10
            while True:
                if (scratch / "retirement-wait").exists():
                    break
                assert time.monotonic() < deadline, "aborted stream never reached retirement gate"
                time.sleep(0.01)
            try:
                receipts.append(turn(46))
                assert not (scratch / "retirement-released").exists(), "cleanup join was not held"
            finally:
                (scratch / "release-retirement").touch()
        finally:
            (scratch / "http.json").write_text(json.dumps(receipts, indent=2))
            (scratch / "release-retirement").touch()
            host.terminate()
            try:
                host.wait(timeout=30)
            except subprocess.TimeoutExpired:
                host.kill()
                host.wait(timeout=10)
                raise AssertionError("host failed to join on shutdown")
            assert host.returncode == 0, f"host shutdown exit: {host.returncode}"
    peers = [json.loads(line) for line in (scratch / "peer.jsonl").read_text().splitlines()]
    prompts = [row for row in peers if row["method"] == "prompt"]
    assert len(prompts) == 13 and len({row["pid"] for row in prompts}) == 13, "stateless runtime sharing or missing requests"
    assert (scratch / "retirement-released").exists(), "delayed retirement never joined"
    cancellations = [line for line in (scratch / "host.log").read_text().splitlines() if "msg=attempt_canceled " in line]
    assert len(cancellations) == 1 and "cancel_mode=transport" in cancellations[0], "unexpected cancellation outside intentional abort"
    for pid in {row[key] for row in peers for key in ("pid", "supervisor")}:
        try:
            os.kill(pid, 0)
        except ProcessLookupError:
            continue
        raise AssertionError(f"ACP child {pid} survived host shutdown")
    binary = scratch / "plugins/claudeagentacp/bin/aip-backend-claudeagentacp"
    (scratch / "result.json").write_text(json.dumps({"result": "PASS", "requests": len(receipts),
        "certification": "test-only build overlay; NOT live-provider certification",
        "plugin_sha256": hashlib.sha256(binary.read_bytes()).hexdigest()}, indent=2))
    print("PASS: two turns, identical retries, concurrent conversations, fresh work, joined child processes")


if __name__ == "__main__":
    main()
