package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

func main() {
	root := flag.String("root", ".", "repository root")
	asJSON := flag.Bool("json", false, "emit JSON array")
	flag.Parse()
	mods, err := discoverModules(*root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "discover_modules: %v\n", err)
		os.Exit(1)
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(mods)
		return
	}
	for _, m := range mods {
		fmt.Println(m)
	}
}

func discoverModules(root string) ([]string, error) {
	var out []string
	for _, base := range []string{"connectors", "connector-support"} {
		dir := filepath.Join(root, base)
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			mod := filepath.Join(dir, e.Name(), "go.mod")
			if _, err := os.Stat(mod); err != nil {
				continue
			}
			rel := filepath.ToSlash(filepath.Join(base, e.Name()))
			out = append(out, rel)
		}
	}
	sort.Strings(out)
	return out, nil
}
