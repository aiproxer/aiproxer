#!/usr/bin/env python3
"""External ACP peer for run.py; no network, credentials, or Claude SDK."""
import json
import os
from pathlib import Path
import re
import sys


def send(value):
    print(json.dumps(value), flush=True)


def record(method, **fields):
    # One append syscall per record, including across concurrent subprocesses.
    fd = os.open(Path(__file__).with_name("peer.jsonl"), os.O_WRONLY | os.O_CREAT | os.O_APPEND, 0o600)
    try:
        os.write(fd, (json.dumps(dict(pid=os.getpid(), supervisor=os.getppid(), method=method, **fields)) + "\n").encode())
    finally:
        os.close(fd)


def main():
    model = "claude-sonnet-5-5"
    sequence = 0
    ticket = None
    delayed = False
    options = [{"id": "model", "type": "select", "currentValue": model,
                "options": [{"value": model}]},
               {"id": "effort", "type": "select", "currentValue": "low",
                "defaultValue": "low", "options": [{"value": "low"}]}]
    record("start")
    for line in sys.stdin:
        request = json.loads(line)
        method = request.get("method")
        params = request.get("params", {})
        record(method)
        result = {}
        if method == "initialize":
            result = {"protocolVersion": 1}
        elif method == "_aip/models_v1":
            result = {"version": 1, "currentModelId": model, "models": [{"nativeId": model}]}
        elif method == "session/new":
            result = {"sessionId": "fake-session", "configOptions": options}
        elif method == "session/set_config_option":
            result = {"configOptions": options}
        elif method == "_aip/effective_identity_v1":
            sequence += 1
            ticket = {"version": 1, "sessionId": "fake-session", "checkpointId": params["checkpointId"],
                      "processGeneration": "a" * 64, "queryGeneration": 1,
                      "observationSequence": sequence, "provider": "firstParty", "credentialSource": "host_login"}
            result = ticket
        elif method == "session/prompt":
            assert ticket is not None and params["_meta"]["aipIdentity"] == ticket
            ticket = None
            text = "".join(block.get("text", "") for block in params["prompt"])
            record("prompt", text=text)
            delayed = "DELAY_RETIREMENT" in text
            numbers = re.findall(r"Remember the number (\d+)", text)
            assert numbers, "cold request lost its transcript"
            answer = numbers[-1] if "What number" in text else "ok"
            send({"jsonrpc": "2.0", "method": "session/update", "params": {
                "sessionId": "fake-session", "update": {"sessionUpdate": "agent_message_chunk",
                "content": {"type": "text", "text": answer}}}})
            if delayed:
                # The test-only Process.Wait decorator holds the join after
                # real OS retirement. Production supervisor stays unmodified.
                Path(__file__).with_name(str(os.getppid()) + ".hold").touch()
                continue
            result = {"stopReason": "end_turn"}
        elif method == "session/cancel":
            continue
        else:
            raise AssertionError("unexpected ACP method: " + str(method))
        if "id" in request:
            send({"jsonrpc": "2.0", "id": request["id"], "result": result})


if __name__ == "__main__":
    main()
