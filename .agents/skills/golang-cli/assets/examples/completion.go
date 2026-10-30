package main

import "github.com/spf13/cobra"

// === Shell Completion Command ===
// Cobra generates completions for bash, zsh, fish, and PowerShell automatically.

func init() {
	rootCmd.AddCommand(&cobra.Command{
		Use:       "completion [bash|zsh|fish|powershell]",
		Short:     "Generate shell completion script",
		Args:      cobra.ExactValidArgs(1),
		ValidArgs: []string{"bash", "zsh", "fish", "powershell"},
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			switch args[0] {
			case "bash":
				return rootCmd.GenBashCompletionV2(out, true)
			case "zsh":
				return rootCmd.GenZshCompletion(out)
			case "fish":
				return rootCmd.GenFishCompletion(out, true)
			case "powershell":
				return rootCmd.GenPowerShellCompletionWithDesc(out)
			}
			return nil
		},
	})
}

// === Custom Completions ===
// Add custom completions for flags and arguments.

func customCompletionExamples() {
	deployCmd.RegisterFlagCompletionFunc("env", func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		return []string{
			"dev\tDevelopment environment",
			"staging\tStaging environment",
			"prod\tProduction environment",
		}, cobra.ShellCompDirectiveNoFileComp
	})

	// Dynamic argument completion
	deployCmd.ValidArgsFunction = func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if len(args) != 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		return getAvailableServices(), cobra.ShellCompDirectiveNoFileComp
	}
}

func getAvailableServices() []string {
	// fetch available services dynamically
	return nil
}
