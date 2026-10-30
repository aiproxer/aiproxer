package main

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/spf13/cobra"
)

func executeCommand(root *cobra.Command, args ...string) (string, string, error) {
	out, diagnostics := new(bytes.Buffer), new(bytes.Buffer)
	root.SetOut(out)
	root.SetErr(diagnostics)
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), diagnostics.String(), err
}

func newTestServeCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use: "serve", Args: cobra.NoArgs, SilenceUsage: true, SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			port, err := cmd.Flags().GetInt("port")
			if err != nil {
				return err
			}
			if port < 1 || port > 65535 {
				return fmt.Errorf("port out of range")
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "listening on :%d\n", port)
			return err
		},
	}
	cmd.Flags().Int("port", 8080, "port to listen on")
	return cmd
}

func TestServeCommand(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    string
		wantErr bool
	}{
		{name: "default port", want: "listening on :8080\n"},
		{name: "custom port", args: []string{"--port", "9090"}, want: "listening on :9090\n"},
		{name: "invalid port", args: []string{"--port", "0"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, diagnostics, err := executeCommand(newTestServeCommand(), tt.args...)
			if (err != nil) != tt.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("output = %q, want %q", got, tt.want)
			}
			if diagnostics != "" {
				t.Errorf("unexpected diagnostics: %q", diagnostics)
			}
		})
	}
}
