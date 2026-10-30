package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/accessmode"
	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/db"
	"github.com/aiproxer/aiproxer/internal/infra/dbmigrate"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/stdhttp"
)

func printAipstdUsage(fs *flag.FlagSet) {
	_, _ = fmt.Fprintf(
		fs.Output(),
		"Usage: aipstd [--config path] [serve|check-config|routes|inventory|inspect|doctor|migrate]\n\n",
	)
	fs.PrintDefaults()
}

type CommandName string

const (
	CommandServe       CommandName = "serve"
	CommandCheckConfig CommandName = "check-config"
	CommandRoutes      CommandName = "routes"
	CommandInventory   CommandName = "inventory"
	CommandInspect     CommandName = "inspect"
	CommandDoctor      CommandName = "doctor"
	CommandMigrate     CommandName = "migrate"
)

type CommandOptions struct {
	Name           CommandName
	ConfigPath     string
	StreamRecovery config.StreamRecoveryOverrides
	MultiUser      *bool
	Output         io.Writer
	ErrorOut       io.Writer
	Components     string
	InstanceID     string
}

type ParsedArgs struct {
	ConfigPath     string
	Name           CommandName
	StreamRecovery config.StreamRecoveryOverrides
	MultiUser      *bool
	Components     string
	InstanceID     string
}

func RunCommand(ctx context.Context, opts CommandOptions) int {
	if opts.Output == nil {
		opts.Output = os.Stdout
	}
	if opts.ErrorOut == nil {
		opts.ErrorOut = os.Stderr
	}
	if ctx == nil {
		_, _ = fmt.Fprintln(opts.ErrorOut, "aipstd: nil context")
		return 2
	}
	switch opts.Name {
	case CommandServe:
		return runServeCommand(ctx, opts)
	case CommandCheckConfig:
		return runCheckConfigCommand(ctx, opts)
	case CommandRoutes:
		return runRoutesCommand(ctx, opts)
	case CommandInventory:
		return runInventoryCommand(ctx, opts)
	case CommandInspect:
		return runInspectCommand(ctx, opts)
	case CommandDoctor:
		return runDoctorCommand(ctx, opts)
	case CommandMigrate:
		return runMigrateCommand(ctx, opts)
	default:
		_, _ = fmt.Fprintf(opts.ErrorOut, "aipstd: unknown command %q\n", opts.Name)
		return 2
	}
}

const migrationPostgresDSNEnv = "AIP_MIGRATION_POSTGRES_DSN"

var runPostgresMigrate = dbmigrate.PostgresComponents

func runMigrateCommand(ctx context.Context, opts CommandOptions) int {
	components, err := dbmigrate.ParseComponents(opts.Components)
	if err != nil {
		_, _ = fmt.Fprintf(opts.ErrorOut, "aipstd migrate: %v\n", err)
		return 2
	}
	dsn := strings.TrimSpace(os.Getenv(migrationPostgresDSNEnv))
	if dsn == "" {
		_, _ = fmt.Fprintf(opts.ErrorOut, "aipstd migrate: set %s\n", migrationPostgresDSNEnv)
		return 1
	}
	child, cancel := context.WithTimeout(ctx, db.DefaultPostgresOpenMigrateTimeout)
	defer cancel()
	if err := runPostgresMigrate(child, dsn, components); err != nil {
		_, _ = fmt.Fprintf(opts.ErrorOut, "aipstd migrate: %v\n", err)
		return 1
	}
	return 0
}

func parseCommandName(args []string) (CommandName, error) {
	if len(args) == 0 {
		return CommandServe, nil
	}
	switch args[0] {
	case string(CommandServe):
		return CommandServe, nil
	case string(CommandCheckConfig):
		return CommandCheckConfig, nil
	case string(CommandRoutes):
		return CommandRoutes, nil
	case string(CommandInventory):
		return CommandInventory, nil
	case string(CommandInspect):
		return CommandInspect, nil
	case string(CommandDoctor):
		return CommandDoctor, nil
	case string(CommandMigrate):
		return CommandMigrate, nil
	default:
		return "", fmt.Errorf("unknown command %q", args[0])
	}
}

func parseCLIPrefix(argv []string) (prefixArgs []string, name CommandName, tail []string) {
	i := 0
	for i < len(argv) {
		a := argv[i]
		if flagTakesValue(a) {
			prefixArgs = append(prefixArgs, a)
			i++
			if i < len(argv) && !hasInlineFlagValue(a) {
				prefixArgs = append(prefixArgs, argv[i])
				i++
			}
			continue
		}
		switch CommandName(a) {
		case CommandServe, CommandCheckConfig, CommandRoutes, CommandInventory, CommandInspect, CommandDoctor, CommandMigrate:
			return prefixArgs, CommandName(a), argv[i+1:]
		default:
			prefixArgs = append(prefixArgs, a)
			i++
		}
	}
	return prefixArgs, CommandServe, []string{}
}

func flagTakesValue(a string) bool {
	switch a {
	case "-config", "--config", "-auto-resume", "--auto-resume", "-auto-resume-idle-timeout", "--auto-resume-idle-timeout", "-auto-resume-grace-period", "--auto-resume-grace-period", "-instance", "--instance", "-components", "--components":
		return true
	default:
		return hasInlineFlagValue(a)
	}
}

// hasInlineFlagValue recognizes embedded values without misclassifying positionals.
func hasInlineFlagValue(a string) bool {
	return len(a) > 0 && a[0] == '-' && strings.Contains(a, "=")
}

func ParseArgsFull(argv []string, usageOut io.Writer) (ParsedArgs, error) {
	if usageOut == nil {
		usageOut = io.Discard
	}
	prefixArgs, name, tail := parseCLIPrefix(argv)
	out := ParsedArgs{ConfigPath: "./config/config.yaml", Name: name}
	if err := parseCommandFlags("aipstd", prefixArgs, usageOut, &out); err != nil {
		return ParsedArgs{}, err
	}
	if len(tail) > 0 {
		if err := parseCommandFlags(string(name), tail, usageOut, &out); err != nil {
			return ParsedArgs{}, err
		}
	}
	return out, nil
}

func parseCommandFlags(name string, args []string, usageOut io.Writer, out *ParsedArgs) error {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(usageOut)
	var autoResume string
	var idleTimeout string
	var gracePeriod string
	fs.StringVar(&out.ConfigPath, "config", out.ConfigPath, "path to runtime config")
	fs.StringVar(&autoResume, "auto-resume", "", "enable stream auto-resume/recovery")
	fs.StringVar(&idleTimeout, "auto-resume-idle-timeout", "", "auto-resume idle timeout")
	fs.StringVar(&gracePeriod, "auto-resume-grace-period", "", "auto-resume grace period")
	fs.StringVar(&out.Components, "components", out.Components, "comma-separated migration components")
	fs.StringVar(&out.InstanceID, "instance", out.InstanceID, "configured backend instance id for doctor")
	var multiUser bool
	fs.BoolVar(&multiUser, "multi-user", false, "opt in to access.mode multi_user for serve")
	fs.Usage = func() { printAipstdUsage(fs) }
	if err := fs.Parse(args); err != nil {
		return err
	}
	if extra := fs.Args(); len(extra) > 0 {
		return fmt.Errorf("unexpected arguments: %v", extra)
	}
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "multi-user" {
			v := multiUser
			out.MultiUser = &v
		}
	})
	if autoResume != "" {
		v, err := parseBoolFlag("auto-resume", autoResume)
		if err != nil {
			return err
		}
		out.StreamRecovery.CLIEnabled = &v
	}
	if idleTimeout != "" {
		d, err := time.ParseDuration(idleTimeout)
		if err != nil || d <= 0 {
			return fmt.Errorf("auto-resume-idle-timeout: invalid positive duration %q", idleTimeout)
		}
		out.StreamRecovery.CLIIdleTimeout = d
	}
	if gracePeriod != "" {
		d, err := time.ParseDuration(gracePeriod)
		if err != nil || d <= 0 {
			return fmt.Errorf("auto-resume-grace-period: invalid positive duration %q", gracePeriod)
		}
		out.StreamRecovery.CLIGracePeriod = d
	}
	return nil
}

func parseBoolFlag(name, raw string) (bool, error) {
	switch raw {
	case "true", "1", "t", "TRUE", "True":
		return true, nil
	case "false", "0", "f", "FALSE", "False":
		return false, nil
	default:
		return false, fmt.Errorf("%s: invalid boolean %q", name, raw)
	}
}

func runServeCommand(ctx context.Context, opts CommandOptions) int {
	compose := stdhttp.ComposeStandardHTTP
	host, err := runtimebundle.BuildHost(ctx, runtimebundle.BuildHostInput{
		ConfigPath:              opts.ConfigPath,
		Mandatory:               mandatoryStandardPlugins(),
		LogWriter:               opts.Output,
		StreamRecoveryOverrides: opts.StreamRecovery,
		HandlerComposer:         compose,
		EnterpriseCapabilities:  stockEnterpriseCapabilities(),
		EnforceMultiUserCLIGate: true,
		MultiUser:               opts.MultiUser,
	})
	if err != nil {
		if errors.Is(err, accessmode.ErrMultiUserFlagRequired) || errors.Is(err, accessmode.ErrMultiUserFlagInconsistent) {
			_, _ = fmt.Fprintf(opts.ErrorOut, "aipstd: %v\n", err)
			return 2
		}
		_, _ = fmt.Fprintf(opts.ErrorOut, "bootstrap failed: %v\n", err)
		return 1
	}
	setProcessDefaultLogger(host.Logger())
	if err := logBootstrapAccessAuth(ctx, host.Logger(), host.Config()); err != nil {
		cleanupErr := closeServeHostAfterBuild(ctx, host, nil)
		host.Logger().ErrorContext(ctx, "aipstd: bootstrap access/auth", "error", errors.Join(err, cleanupErr))
		return 1
	}
	mgmt, err := startManagementServer(ctx, host.Config(), host.Logger(), host)
	if err != nil {
		cleanupErr := closeServeHostAfterBuild(ctx, host, nil)
		host.Logger().ErrorContext(ctx, "aipstd: management server", "error", errors.Join(err, cleanupErr))
		return 1
	}
	sigCtx, stop := startServeSignalHandling(ctx, host)
	defer stop()
	if err := stdhttp.RunWithGenerationHost(sigCtx, stdhttp.GenerationHostInput{
		Config:     host.Config(),
		Log:        host.Logger(),
		Host:       host,
		Management: mgmt,
	}); err != nil {
		host.Logger().ErrorContext(sigCtx, "server stopped", "error", err)
		return 1
	}
	return 0
}

func runCheckConfigCommand(ctx context.Context, opts CommandOptions) int {
	report, err := runtimebundle.ValidateStructural(ctx, runtimebundle.ValidateStructuralInput{
		ConfigPath:              opts.ConfigPath,
		Mandatory:               mandatoryStandardPlugins(),
		StreamRecoveryOverrides: opts.StreamRecovery,
		EnterpriseCapabilities:  stockEnterpriseCapabilities(),
	})
	if err != nil {
		_, _ = fmt.Fprintf(opts.ErrorOut, "configuration invalid: %v\n", err)
		return 1
	}
	if report.DefaultRouteWarning != "" {
		// An advisory, not a refusal: the configuration serves the clients that
		// route explicitly, and the operator is told what a client without an
		// explicit route will find.
		_, _ = fmt.Fprintf(opts.ErrorOut, "warning: %s\n", report.DefaultRouteWarning)
	}
	_, _ = fmt.Fprintln(opts.Output, "configuration is valid")
	return 0
}

func runRoutesCommand(ctx context.Context, opts CommandOptions) int {
	snap, err := runtimebundle.InspectRoutes(ctx, runtimebundle.InspectInput{
		ConfigPath:              opts.ConfigPath,
		Mandatory:               mandatoryStandardPlugins(),
		StreamRecoveryOverrides: opts.StreamRecovery,
		EnterpriseCapabilities:  stockEnterpriseCapabilities(),
	})
	if err != nil {
		_, _ = fmt.Fprintf(opts.ErrorOut, "bootstrap failed: %v\n", err)
		return 1
	}
	enc := json.NewEncoder(opts.Output)
	enc.SetIndent("", "  ")
	if err := enc.Encode(snap); err != nil {
		_, _ = fmt.Fprintf(opts.ErrorOut, "routes: encode: %v\n", err)
		return 1
	}
	return 0
}

func runInventoryCommand(ctx context.Context, opts CommandOptions) int {
	snap, err := runtimebundle.InspectInventory(ctx, runtimebundle.InspectInput{
		ConfigPath:              opts.ConfigPath,
		Mandatory:               mandatoryStandardPlugins(),
		StreamRecoveryOverrides: opts.StreamRecovery,
		EnterpriseCapabilities:  stockEnterpriseCapabilities(),
	})
	if err != nil {
		_, _ = fmt.Fprintf(opts.ErrorOut, "bootstrap failed: %v\n", err)
		return 1
	}
	enc := json.NewEncoder(opts.Output)
	enc.SetIndent("", "  ")
	if err := enc.Encode(snap); err != nil {
		_, _ = fmt.Fprintf(opts.ErrorOut, "inventory: encode: %v\n", err)
		return 1
	}
	return 0
}

func runInspectCommand(ctx context.Context, opts CommandOptions) int {
	prep, err := runtimebundle.PrepareInspect(ctx, runtimebundle.InspectInput{
		ConfigPath:              opts.ConfigPath,
		Mandatory:               mandatoryStandardPlugins(),
		StreamRecoveryOverrides: opts.StreamRecovery,
		EnterpriseCapabilities:  stockEnterpriseCapabilities(),
	})
	if err != nil {
		_, _ = fmt.Fprintf(opts.ErrorOut, "bootstrap failed: %v\n", err)
		return 1
	}
	defer func() { _ = prep.Close() }()
	rep, inspectErr := runtimebundle.InspectBackendPluginsWithContext(ctx, prep.Config, prep.Registry)
	enc := json.NewEncoder(opts.Output)
	enc.SetIndent("", "  ")
	if err := enc.Encode(rep); err != nil {
		_, _ = fmt.Fprintf(opts.ErrorOut, "inspect: encode: %v\n", err)
		return 1
	}
	if inspectErr != nil {
		_, _ = fmt.Fprintf(opts.ErrorOut, "inspect: %v\n", inspectErr)
		return 1
	}
	return 0
}

func runDoctorCommand(ctx context.Context, opts CommandOptions) int {
	instanceID := strings.TrimSpace(opts.InstanceID)
	if instanceID == "" {
		_, _ = fmt.Fprintln(opts.ErrorOut, "aipstd doctor: --instance <configured-backend-id> is required")
		return 2
	}
	prep, err := runtimebundle.PrepareInspect(ctx, runtimebundle.InspectInput{
		ConfigPath:              opts.ConfigPath,
		Mandatory:               mandatoryStandardPlugins(),
		StreamRecoveryOverrides: opts.StreamRecovery,
		EnterpriseCapabilities:  stockEnterpriseCapabilities(),
	})
	if err != nil {
		_, _ = fmt.Fprintf(opts.ErrorOut, "bootstrap failed: %v\n", err)
		return 1
	}
	defer func() { _ = prep.Close() }()
	rep, err := runtimebundle.DoctorBackendPlugin(ctx, prep.Config, prep.Registry, instanceID, prep.PluginHost())
	if err != nil {
		_, _ = fmt.Fprintf(opts.ErrorOut, "doctor: %v\n", err)
		return 1
	}
	enc := json.NewEncoder(opts.Output)
	enc.SetIndent("", "  ")
	if err := enc.Encode(rep); err != nil {
		_, _ = fmt.Fprintf(opts.ErrorOut, "doctor: encode: %v\n", err)
		return 1
	}
	for _, r := range rep.Results {
		if r.State != "active" && r.Reason != "builtin" {
			return 1
		}
	}
	return 0
}
