package main

import "io"

func ParseArgs(argv []string, usageOut io.Writer) (configPath string, name CommandName, err error) {
	parsed, err := ParseArgsFull(argv, usageOut)
	if err != nil {
		return "", "", err
	}
	return parsed.ConfigPath, parsed.Name, nil
}
