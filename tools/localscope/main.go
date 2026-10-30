// Command localscope exposes the shared direct scope plan to shell adapters.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/aiproxer/aiproxer/tools/internal/scopeplan"
)

func main() {
	root := flag.String("root", ".", "repository root")
	mode := flag.String("mode", "staged", "staged, changed, base, branch, or explicit")
	base := flag.String("base", "origin/main", "branch comparison base")
	metadata := flag.Bool("metadata", true, "expand affected module metadata to its full module")
	format := flag.String("format", "json", "json, nul records, or module/package lines")
	module := flag.String("module", ".", "explicit module directory")
	packages := flag.String("packages", "", "explicit space-separated relative package patterns")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	var err error
	var plan scopeplan.Plan
	if *mode == "explicit" {
		plan, err = scopeplan.Explicit(*root, *module, strings.Fields(*packages))
	} else {
		var paths []string
		paths, _, err = scopeplan.Changes(ctx, *root, *mode, *base)
		if err == nil {
			plan, err = scopeplan.Direct(*root, paths, *metadata)
		}
	}
	if err == nil {
		switch *format {
		case "json":
			err = json.NewEncoder(os.Stdout).Encode(plan)
		case "lines":
			for _, module := range plan.Modules {
				fmt.Printf("%s\t%s\n", module.Directory, strings.Join(module.Packages, " "))
			}
		case "nul":
			for _, name := range plan.Paths {
				fmt.Printf("path\x00%s\x00\x00", name)
			}
			for _, name := range plan.ExistingGo {
				fmt.Printf("file\x00%s\x00\x00", name)
			}
			for _, module := range plan.Tidy {
				fmt.Printf("tidy\x00%s\x00\x00", module)
			}
			for _, module := range plan.Modules {
				for _, pkg := range module.Packages {
					fmt.Printf("package\x00%s\x00%s\x00", module.Directory, pkg)
				}
			}
		default:
			err = fmt.Errorf("unknown format %q", *format)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
