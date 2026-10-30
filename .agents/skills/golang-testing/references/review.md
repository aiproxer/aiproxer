# Reviewing tests

Stay read-only unless fixes are requested. Use the claim, source, and mutant definitions and the dummy-test table from `SKILL.md`.

1. **List the tests in scope** (changed test files, plus existing tests for changed production code).
2. **For each test, write its claim, source, and mutant.** If you cannot name all three, that test is a finding. Match it to a row of the dummy-test table and recommend that row's "Do instead".
3. **Check that each mutant would turn the test red.** Read the assertions: a test passes vacuously when it discards results, asserts only `err == nil`, compares against values computed by the code under test, or is skipped/tagged out of every documented command.
4. **List missing claims.** For the changed production code, name the failure, cancellation, partial-result, and authorization behaviour that no test defends.
5. **Check isolation**: shared package-level state, process env (`t.Setenv` vs `os.Setenv`), `t.Parallel` with shared fixtures, map iteration order, goroutines asserted before synchronization.
6. **Check fuzz and benchmark oracles**: a fuzz oracle states a contract property, not "no error"; a benchmark times the success path on representative state.

Report each finding as: severity, file and test name, the claim at risk (or "no claim"), and the smallest remedy, which is often deletion. Separate defects the change introduced from pre-existing debt. State which commands you ran and which conclusions are inferred from reading.

Completion criterion: every test in scope has either a stated claim, source, and mutant, or a finding.
