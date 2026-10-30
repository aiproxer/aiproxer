# Debugging methodology

Use the sequence in [the skill entrypoint](../SKILL.md): capture the symptom, reproduce it, isolate one hypothesis, gather evidence, fix the cause, and verify a regression test.

## Evidence record

Record revision, Go/toolchain version, OS/architecture, command, inputs, expected/actual output, timing, and relevant environment. Keep sensitive values redacted. If the issue is intermittent, record frequency and a reproducible stress command.

## Hypothesis loop

Write one claim that could be false. Choose one measurement that distinguishes it from the next likely cause. Change one variable, rerun the reproduction, and preserve both positive and negative results. Trace callers and ownership before declaring a local line the root cause.

## Fix and defense

Add a focused test or invariant check, implement the smallest fix, inspect the diff, and run focused plus relevant broad checks. For performance, compare same-workload profiles or benchmark distributions. For concurrency, use cancellation tests and the race detector where supported. Report unresolved environmental uncertainty rather than treating a green local run as proof.
