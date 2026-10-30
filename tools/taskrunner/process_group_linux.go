//go:build linux

package taskrunner

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// processGroupHasLiveMembers reports whether any non-zombie process belongs to
// pgid. Linux keeps a killed, unreaped process in its process group, so the
// kill(-pgid, 0) existence probe alone cannot distinguish a survivor from a
// zombie. /proc exposes the state needed for that distinction without reaping
// processes the runner does not own.
func processGroupHasLiveMembers(pgid int) (bool, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue
		}
		data, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "stat"))
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return false, err
		}
		// The comm field is parenthesised and may contain spaces and
		// parentheses. Everything after the final ')' is: state, ppid, pgrp.
		index := bytes.LastIndexByte(data, ')')
		if index < 0 || index+2 >= len(data) {
			continue
		}
		fields := strings.Fields(string(data[index+2:]))
		if len(fields) < 3 {
			continue
		}
		group, err := strconv.Atoi(fields[2])
		if err != nil || group != pgid {
			continue
		}
		switch fields[0] {
		case "Z", "X", "x":
			continue
		}
		return true, nil
	}
	return false, nil
}
