---
name: golang-stretchr-testify
description: "Use and review stretchr/testify assertions, fatal guards, error matching, mocks, suites, polling, goroutine constraints, and diagnostics."
---

# stretchr/testify

Testify complements testing.T; it does not replace the standard test runner. Pin the module version and confirm assertion or mock signatures in that version.

## Focused review

For a review, keep the work read-only unless fixes are requested. Establish the review base, supported versions, and relevant contract; inspect changed code plus the callers and tests needed to assess it. Stay within this skill’s lens.

- Check fatal guards protect subsequent dereferences and run in the test goroutine; require/FailNow in spawned goroutines cannot abort the parent test safely.
- Trace mocks for argument specificity, typed-nil returns, pointer mutation after calls, expectation cardinality, and assertions registered before early failure.
- Inspect shared suite state and unsupported suite parallelism, test-owned cleanup, polling synchronization, and diagnostic quality on timeout.
- Compare assertions to the contract: pointer identity versus deep equality, error identity versus text, ordered versus unordered values, and approximate versus exact results.

Report each actionable finding with severity, confidence, file/symbol, trigger, consequence, and smallest remedy. Separate introduced/worsened defects from pre-existing debt, and state executed checks versus inference. If none survives validation, say so and identify coverage gaps.

## assert and require

Use require for setup/preconditions where continuing would panic or produce misleading failures. Use assert for independent checks whose failures should accumulate. Both use expected, actual argument order. require/FailNow must run in the test goroutine; send worker errors back for assertions there. Testify suites do not support parallel tests, even if individual cases appear isolated.

~~~go
func TestParse(t *testing.T) {
    must := require.New(t)
    is := assert.New(t)

    got, err := Parse("valid")
    must.NoError(err)
    must.NotNil(got)
    is.Equal("valid", got.Name)
}
~~~

Equal uses deep equality, including the values pointed to by pointers; it does not assert pointer identity. Use assert.Same when the same object address is the contract, and compare fields or values when identity is irrelevant. Use ErrorIs/ErrorAs for wrapped errors, InDelta/WithinDuration for measured values, ElementsMatch for unordered collections, and JSONEq only when JSON semantic equality is intended.

Eventually and EventuallyWithT are bounded polling helpers. Keep the timeout realistic, make the poll function safe, and include a final diagnostic. They do not make an eventually-consistent system deterministic; choose a readiness/observation contract.

## Mocks

A mock embeds mock.Mock and delegates methods through Called. Match only behavior relevant to the test. Prefer mock.AnythingOfType or MatchedBy when exact values are not the contract, but avoid broad matchers that hide wrong arguments. Use Once/Times/Maybe/Run deliberately and call AssertExpectations in teardown or at the end of the test.

Do not make a mock more permissive than the production interface. Preserve return arity, typed nil behavior, context arguments, and error identity. A fake may be clearer than a mock for stateful repositories.

## Suites

A suite is optional organization around testing.T. SetupSuite and TearDownSuite run once; SetupTest and TearDownTest run around each test. The launcher is required:

~~~go
func TestAccountSuite(t *testing.T) {
    suite.Run(t, new(AccountSuite))
}
~~~

Keep suite state isolated and use ordinary testing.T tests when parallel execution is needed. Plain table-driven tests are often simpler.

## Review checklist

Check assertion order, fatal versus non-fatal guards, pointer identity versus equality, wrapped errors, matcher specificity, expectation verification, cleanup, goroutine leaks, and bounded polling. Run gofmt and focused go test. Use testifylint or another linter only as an aid; it does not replace reviewing the behavior contract.
