#!/usr/bin/env python3
"""Credential-free acceptance through a built aipstd process; see docs/testing/cache-affinity-process-e2e.md."""
import argparse
import copy
import hashlib
import http.client
import http.server
import json
import os
from pathlib import Path
import socket
import subprocess
import threading
import time
from urllib.parse import urlsplit


def write_json(path, value):
    path.write_text(json.dumps(value, indent=2) + "\n")


def require(ok, message):
    if not ok:
        raise AssertionError(message)


def declared_toolchain(root):
    """Return the toolchain the repository declares in go.mod.

    The acceptance builds the distribution with that exact toolchain, so the
    requirement follows the repository instead of a copy of its version: a
    security-patched toolchain bump must not leave this script demanding the
    vulnerable one.
    """
    for line in (root / "go.mod").read_text().splitlines():
        parts = line.split()
        if len(parts) == 2 and parts[0] == "toolchain":
            return parts[1]
    raise AssertionError("go.mod declares no toolchain")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--evidence", required=True, type=Path)
    parser.add_argument("--mutant", action="store_true", help="disable transform contribution in build overlay")
    args = parser.parse_args()
    root = Path(__file__).resolve().parents[1]
    out = args.evidence.resolve()
    out.mkdir(parents=True, exist_ok=False)
    env = {k: os.environ[k] for k in ("PATH", "HOME", "TMPDIR", "GOTOOLCHAIN") if k in os.environ}
    toolchain = declared_toolchain(root)
    require(env.get("GOTOOLCHAIN") == toolchain, f"set GOTOOLCHAIN to repo-declared {toolchain}")
    require(env.get("TMPDIR") == "/var/tmp/aip", "set TMPDIR=/var/tmp/aip")
    captures, commands, checks, private, client_calls = [], [], [], [], []
    result = {"mutant": args.mutant, "commands": commands, "checks": checks, "client_calls": client_calls, "environment": env}
    process = None

    class Provider(http.server.BaseHTTPRequestHandler):
        def log_message(self, *_):
            pass

        def do_GET(self):
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(b'{"data":[{"id":"acceptance-model"}]}')

        def do_POST(self):
            body = self.rfile.read(int(self.headers["Content-Length"]))
            captures.append({"path": self.path, "headers": dict(self.headers), "body": body.decode(), "json": json.loads(body)})
            if not json.loads(body).get("stream", False):
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.end_headers()
                if self.path.endswith("/chat/completions"):
                    reply = {"id": "chatcmpl-acceptance", "object": "chat.completion", "created": 1, "model": "acceptance-model", "choices": [{"index": 0, "message": {"role": "assistant", "content": "process acceptance reply"}, "finish_reason": "stop"}]}
                else:
                    reply = {"id": "resp_acceptance", "object": "response", "status": "completed", "model": "acceptance-model", "output": [{"id": "msg_acceptance", "type": "message", "role": "assistant", "status": "completed", "content": [{"type": "output_text", "text": "process acceptance reply", "annotations": []}]}]}
                self.wfile.write(json.dumps(reply).encode())
                return
            self.send_response(200)
            self.send_header("Content-Type", "text/event-stream")
            self.end_headers()
            if self.path.endswith("/chat/completions"):
                events = 'data: {"id":"chatcmpl-acceptance","object":"chat.completion.chunk","model":"acceptance-model","choices":[{"index":0,"delta":{"role":"assistant","content":"process acceptance reply"},"finish_reason":null}]}\n\ndata: {"id":"chatcmpl-acceptance","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}\n\ndata: [DONE]\n\n'
            else:
                response = {"id": "resp_acceptance", "object": "response", "status": "completed", "model": "acceptance-model", "output": [{"id": "msg_acceptance", "type": "message", "role": "assistant", "status": "completed", "content": [{"type": "output_text", "text": "process acceptance reply", "annotations": []}]}]}
                events = ""
                for event in [{"type": "response.created", "response": {**response, "status": "in_progress", "output": []}}, {"type": "response.output_text.delta", "item_id": "msg_acceptance", "output_index": 0, "content_index": 0, "delta": "process acceptance reply"}, {"type": "response.completed", "response": response}]:
                    events += "event: " + event["type"] + "\ndata: " + json.dumps(event) + "\n\n"
            self.wfile.write(events.encode())

    provider = http.server.HTTPServer(("127.0.0.1", 0), Provider)
    worker = threading.Thread(target=provider.serve_forever)
    worker.start()
    try:
        catalog_path = root / "internal/providerprofiles/catalog.json"
        catalog = json.loads(catalog_path.read_text())
        profiles = {p["id"]: p for p in catalog}
        cases = []
        for name in ("mistral", "deepinfra"):
            profile = profiles[name]
            require(profile["quirks"] == ["openai.prompt_cache_key"], "catalog quirk drift")
            original = profile["endpoint"]["base_url"]
            profile["endpoint"]["base_url"] = f"http://127.0.0.1:{provider.server_port}" + urlsplit(original).path
            # No credentials from the caller: a known emulator-only key replaces env auth.
            env[profile["auth"]["env_var"]] = "emulator-only-not-a-secret"
            cases.append((name, "chat", True, urlsplit(original).path))
            synthetic = copy.deepcopy(profile)
            synthetic["id"] = name + "-synthetic-responses"
            synthetic["family"] = "openai-responses-compatible"
            catalog.append(synthetic)
            cases.append((synthetic["id"], "responses", True, urlsplit(original).path))
        unsupported = copy.deepcopy(profiles["mistral"])
        unsupported["id"] = "unsupported-profile"
        unsupported["quirks"] = []
        catalog.append(unsupported)
        cases.extend([("unsupported-profile", "chat", False, "/v1"), ("custom-chat", "chat", False, "/v1"), ("custom-responses", "responses", False, "/v1")])
        write_json(out / "catalog.json", catalog)
        overlay = {str(catalog_path): str(out / "catalog.json")}
        transform = root / "internal/plugins/features/downstreamcacheaffinity/transform.go"
        original_transform = transform.read_bytes()
        if args.mutant:
            source = original_transform.decode()
            require(source.count("call.PromptCacheKey = Derive(backend, sid)") == 1, "mutant anchor drift")
            (out / "transform.go").write_text(source.replace("call.PromptCacheKey = Derive(backend, sid)", "// mutant: omit feature contribution"))
            overlay[str(transform)] = str(out / "transform.go")
        write_json(out / "overlay.json", {"Replace": overlay})
        binary = out / "aipstd"
        argv = ["go", "build", "-buildvcs=false", "-overlay=" + str(out / "overlay.json"), "-o", str(binary), "./cmd/aipstd"]
        with (out / "build.log").open("w") as log:
            built = subprocess.run(argv, cwd=root, env=env, stdout=log, stderr=log, check=False)
        commands.append({"argv": argv, "exit_code": built.returncode})
        require(built.returncode == 0, "actual cmd/aipstd build failed; see build.log")
        result["binary_sha256"] = hashlib.sha256(binary.read_bytes()).hexdigest()
        result["production_transform_sha256"] = hashlib.sha256(original_transform).hexdigest()
        for enabled in (True, False):
            with socket.socket() as sock:
                sock.bind(("127.0.0.1", 0))
                port = sock.getsockname()[1]
            backends = [{"id": name, "kind": "provider-profile", "enabled": True, "config": {"profile": name}} for name, _, _, _ in cases if not name.startswith("custom-")]
            for family in ("chat", "responses"):
                backends.append({"id": "custom-" + family, "kind": "custom-openai-" + ("legacy" if family == "chat" else family) + "-compatible", "enabled": True, "config": {"backend_prefix": "custom-" + family, "base_url": f"http://127.0.0.1:{provider.server_port}/v1"}})
            config = {"server": {"address": f"127.0.0.1:{port}"}, "access": {"mode": "single_user"}, "auth": {"event_delivery": "disabled"}, "logging": {"level": "error", "format": "json"}, "diagnostics": {"enabled": True}, "routing": {"max_attempts": 1, "default_route": "mistral:acceptance-model"}, "continuity": {"store": "memory", "in_memory": True}, "plugins": {"frontends": [{"id": "openai-legacy", "enabled": True, "config": {}}, {"id": "openai-responses", "enabled": True, "config": {}}], "backends": backends, "features": ([{"id": "downstream-cache-affinity", "enabled": True, "config": {}}] if enabled else [])}}
            cfg = out / ("enabled.json" if enabled else "default-off.json")
            write_json(cfg, config)
            argv = [str(binary), "serve", "--config", str(cfg)]
            log = (out / ("enabled.log" if enabled else "default-off.log")).open("w")
            process = subprocess.Popen(argv, cwd=out, env=env, stdout=log, stderr=log)
            commands.append({"argv": argv, "pid": process.pid})
            deadline = time.monotonic() + 30
            while True:
                require(process.poll() is None, "aipstd exited before ready; see process log")
                try:
                    connection = http.client.HTTPConnection("127.0.0.1", port, timeout=1)
                    connection.request("GET", "/healthz")
                    response = connection.getresponse()
                    ready = response.status == 200
                    response.read()
                    connection.close()
                    if ready:
                        break
                except OSError:
                    pass
                require(time.monotonic() < deadline, "aipstd readiness timeout")
                time.sleep(0.05)  # bounded readiness observation, not test synchronization

            def request(name, family, suffix, carriers=None, explicit=None):
                body = {"model": "acceptance-model", "stream": False}
                body.update({"messages": [{"role": "user", "content": "acceptance"}]} if family == "chat" else {"input": "acceptance"})
                if explicit:
                    body["prompt_cache_key"] = explicit
                headers = {"Content-Type": "application/json", "X-AIP-Route": name + ":acceptance-model"}
                if carriers:
                    headers.update(carriers)
                    body["metadata"] = {"aip_session_id": carriers["X-AIP-Session-ID"], "aip_resume_token": carriers["X-AIP-Resume-Token"]}
                safe_body = copy.deepcopy(body)
                if carriers:
                    safe_body["metadata"] = {key: "[REDACTED]" for key in body["metadata"]}
                client_calls.append({"case": f"{enabled}/{name}/{suffix}", "address": f"127.0.0.1:{port}", "json": safe_body, "headers": {k: "[REDACTED]" if k in (carriers or {}) else v for k, v in headers.items()}})
                before = len(captures)
                connection = http.client.HTTPConnection("127.0.0.1", port, timeout=20)
                connection.request("POST", "/v1/chat/completions" if family == "chat" else "/v1/responses", json.dumps(body), headers)
                response = connection.getresponse()
                data = response.read().decode()
                response_headers = dict(response.getheaders())
                connection.close()
                require(response.status == 200, f"{name}/{suffix}: HTTP {response.status}: {data}")
                require("process acceptance reply" in data, f"{name}/{suffix}: missing returning reply")
                lower = {k.lower(): v for k, v in response_headers.items()}
                session = {k: lower[k.lower()] for k in ("X-AIP-Session-ID", "X-AIP-Resume-Token", "X-AIP-A-Leg-ID") if k.lower() in lower}
                if carriers:
                    # BeginResume deliberately issues no new resume proof (manager.go).
                    require(all(carriers[k] == v for k, v in session.items()), "resume returned a different session")
                    session = carriers
                else:
                    require(len(session) == 3, f"{name}/{suffix}: public session carriers missing; STOP, no fabricated session")
                private.extend(session.values())
                require(len(captures) == before + 1, f"{name}/{suffix}: expected one provider request")
                capture = captures[-1]
                capture["case"] = f"{enabled}/{name}/{suffix}"
                require(capture["json"]["model"] == "acceptance-model", "native model namespace mapping")
                checks.append({"case": capture["case"], "http_status": response.status, "reply": json.loads(data), "session_carriers_present": True})
                return session, capture

            for name, family, supported, base_path in cases:
                session, first = request(name, family, "create")
                resumed, second = request(name, family, "resume", session)
                other, third = request(name, family, "different-session")
                _, fourth = request(name, family, "explicit", session, "client-owned-key")
                require(resumed == session, f"{name}: admitted session/continuity not retained")
                require(other["X-AIP-Session-ID"] != session["X-AIP-Session-ID"], "different sessions not issued")
                keys = [c["json"].get("prompt_cache_key") for c in (first, second, third, fourth)]
                if enabled and supported:
                    require(isinstance(keys[0], str) and keys[0].startswith("aipca1_") and len(keys[0]) == 50, f"{name}: missing generated feature contribution: {keys[0]!r}")
                    require(keys[0] == keys[1] and keys[0] != keys[2], f"{name}: session affinity isolation")
                    require(keys[3] == "client-owned-key", f"{name}: explicit key overwritten")
                elif supported:
                    require(keys[:3] == [None] * 3 and keys[3] == "client-owned-key", f"{name}: default-off behavior")
                else:
                    require(keys == [None] * 4, f"{name}: unsupported/custom profile gate")
                for capture in (first, second, third, fourth):
                    require(capture["path"] == base_path + ("/chat/completions" if family == "chat" else "/responses"), "native endpoint path mapping")
                    wire = json.dumps(capture)
                    require(not any(value in wire for value in private), "raw session/token/A-leg leaked downstream")
                    require("aip_session_id" not in wire and "aip_resume_token" not in wire, "session body carriers leaked downstream")
                    require(not any(k.lower().startswith("x-aip-") for k in capture["headers"]), "proxy header leaked downstream")
            process.terminate()
            commands[-1]["exit_code"] = process.wait(timeout=15)
            process = None
            log.close()
        require(transform.read_bytes() == original_transform, "production transform changed")
        result["outcome"] = "passed"
    except Exception as error:
        result.update(outcome="failed", failure=str(error))
        raise
    finally:
        if process is not None:
            process.terminate()
            commands[-1]["exit_code"] = process.wait(timeout=15)
        provider.shutdown()
        worker.join(timeout=5)
        provider.server_close()
        # Store outbound captures only after checking all issued private carriers.
        require(not any(value in json.dumps(captures) for value in private), "refuse unsafe capture persistence")
        write_json(out / "http-captures.json", captures)
        result["captures_asserted_no_session_secrets"] = True
        require(not any(value in json.dumps(result) for value in private), "refuse unsafe client evidence persistence")
        write_json(out / "result.json", result)
        write_json(out / "checksums.json", {p.name: hashlib.sha256(p.read_bytes()).hexdigest() for p in out.iterdir() if p.is_file() and p.name != "checksums.json"})
        print(json.dumps({"outcome": result["outcome"], "checks": len(checks), "evidence": str(out)}))


if __name__ == "__main__":
    main()
