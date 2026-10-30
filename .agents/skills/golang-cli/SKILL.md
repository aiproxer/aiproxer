---
name: golang-cli
description: "Design and review Go command-line behavior: commands, flags, configuration precedence, output, exit status, cancellation, completion, and tests."
---

# Go CLI

Use the smallest command framework that satisfies the product. The standard library flag package is a good fit for a single command with a small flag set. Cobra/pflag is useful for a command tree, generated completion, and rich argument validation. Viper can layer configuration, but it is optional; a typed configuration struct and explicit loaders are often easier to reason about. urfave/cli is another valid framework when the project already uses it. Check the module's current documentation before relying on a version-specific API.

## Focused review

For a review, keep the work read-only unless fixes are requested. Establish the review base, supported versions, and relevant contract; inspect changed code plus the callers and tests needed to assess it. Stay within this skill’s lens.

- Trace one invocation from argument parsing through config precedence, validation, execution, writers, error rendering, cleanup, and exit status. Exercise both usage and operational errors.
- Check repeated execution with fresh command instances: global flags, Viper state, persistent hooks, and reused buffers can leak across commands or tests.
- Follow cancellation through blocking work and shutdown; join owned shutdown goroutines and return cleanup errors before main exits.
- Test redirected and failing writers, JSON output, stdin, and completion. Network calls or config initialization during completion can hang shell users.

Report each actionable finding with severity, confidence, file/symbol, trigger, consequence, and smallest remedy. Separate introduced/worsened defects from pre-existing debt, and state executed checks versus inference. If none survives validation, say so and identify coverage gaps.

## Construction

Keep process startup explicit:

1. main parses the root command result and maps it to a process exit status.
2. Command constructors receive dependencies and return a command; avoid hidden global state.
3. RunE performs validation and work, and returns errors instead of calling os.Exit.
4. The composition root owns logging, configuration loading, signal cancellation, and cleanup.
5. Keep stdout machine-readable and reserve stderr for diagnostics, progress, and logs.

A common layout is cmd/app/main.go plus a package containing command constructors. This is a convention, not a Go requirement. An application with one small command may stay in one package.

Cobra's SilenceUsage and SilenceErrors are policy choices. A common arrangement is SilenceUsage=true and SilenceErrors=true when main prints one controlled diagnostic, but a library or nested command may choose otherwise. Do not enable both and then forget to print the returned error.

## Commands, flags, and arguments

Prefer one constructor per command and register commands explicitly. Persistent flags are inherited; local flags belong to one command. Validate relationships (mutual exclusion, required groups, ranges) before opening resources.

Use framework validators such as NoArgs, ExactArgs, MinimumNArgs, and MaximumNArgs where their contract matches the command. Custom validation should return an actionable error and avoid leaking secrets.

Use the command's configured writers:

~~~go
func newVersionCmd(version string) *cobra.Command {
    cmd := &cobra.Command{
        Use:          "version",
        Args:         cobra.NoArgs,
        SilenceUsage: true,
        RunE: func(cmd *cobra.Command, _ []string) error {
            _, err := fmt.Fprintln(cmd.OutOrStdout(), version)
            return err
        },
    }
    return cmd
}
~~~

Do not write command output directly to os.Stdout or os.Stderr; tests and callers may replace the command writers.

## Configuration

Define a typed config value and validate it after loading. With Viper, the documented precedence from highest to lowest is: explicit Set overrides, bound flags, environment variables, config file, key/value store, and defaults. Treat that order as an integration contract and test the combinations you support.

For Viper:

- set an application-specific env prefix and, if needed, an explicit key replacer;
- bind each flag deliberately; automatic flag discovery is not a substitute for binding;
- configure an optional config file and ignore only ConfigFileNotFoundError;
- decode into a typed struct with mapstructure tags and validate after decode;
- be aware that environment variables are read when requested and an empty value is treated according to Viper's empty-environment setting;
- do not silently merge arbitrary untrusted config into security-sensitive settings.

If the application needs live reload, define ownership, synchronization, validation, and rollback explicitly. A file watcher alone is not a safe reload design.

## Exit status and I/O

Return errors from command handlers. Map categories in main or a small exit-status function. Use 0 for success, 1 for an operational failure, and 2 for usage/validation failures unless the product documents a different contract. BSD sysexits values can be useful when scripts depend on them; they are not a universal requirement. Exit status 128+signal is a shell convention, not something a handler should manufacture.

For output formats, make JSON and other machine-readable modes deterministic and send them to the configured output writer. Never mix progress text with a data stream. Color and terminal detection are optional presentation features and must degrade cleanly when output is redirected.

## Cancellation and shutdown

Use signal.NotifyContext at the process boundary and pass the derived context to operations that can block. A command should stop starting new work after cancellation and should give owned servers, workers, and clients a bounded shutdown period. A long-lived stream may intentionally have no deadline; cancellation and an explicit shutdown policy still apply.

~~~go
ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
defer stop()

if err := run(ctx); err != nil {
    fmt.Fprintln(os.Stderr, err)
    os.Exit(exitCode(err))
}
~~~

Do not call os.Exit before deferred cleanup that must run. If a framework needs to call os.Exit, do so only after the command and cleanup have returned.

## Completion and testing

Use the framework's completion generators, but test custom completion functions against the command's configured context and writers. Completion should be fast, side-effect free, and conservative about network calls.

Test commands through their public execution path with bytes.Buffer writers. Cover invalid args, config precedence, output format, cancellation, and exit classification. Keep integration tests separate when they need a real filesystem, network, or subprocess; do not make a sub-millisecond timing target a correctness rule.

Before changing an existing CLI, inspect its command tree, configuration contract, output conventions, and process lifecycle. Verify with gofmt and focused go test. The examples under assets/examples are deliberately small snippets; adapt their imports and framework versions to the target module.
