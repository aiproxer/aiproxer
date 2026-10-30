package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

type indexEntry struct {
	Artifact string      `json:"artifact"`
	Status   string      `json:"status"`
	Source   SourceState `json:"source"`
}

type executionIndex struct {
	Version              int                   `json:"version"`
	Entries              map[string]indexEntry `json:"entries"`
	Inventory            string                `json:"inventory,omitempty"`
	VerificationAttempts []VerificationAttempt `json:"verification_attempts,omitempty"`
}

// Attempts are append-only command receipts, independent of the latest task
// result. A disposition explains a failure; a passing retry is not one.
type VerificationAttempt struct {
	Record      string `json:"record"`
	Disposition string `json:"disposition,omitempty"`
}

func recordVerificationAttempt(name, record string) error {
	index := executionIndex{Version: 1, Entries: make(map[string]indexEntry)}
	if file, err := os.Open(name); err == nil {
		decodeErr := decodeStrict(file, &index)
		if err := errors.Join(decodeErr, file.Close()); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if index.Version != 1 || index.Entries == nil {
		return errors.New("invalid execution index")
	}
	for _, attempt := range index.VerificationAttempts {
		if attempt.Record == record {
			return errors.New("verification receipt already registered; use a fresh log")
		}
	}
	index.VerificationAttempts = append(index.VerificationAttempts, VerificationAttempt{Record: record})
	if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
		return err
	}
	return writeJSONAtomic(name, index)
}

// The controller alone writes the execution index. Atomic replacement protects
// restart recovery, not concurrent writers; agents write their own result files.
func recordResult(name, artifact string, result Result) (returnErr error) {
	return recordResultWithInventory(name, artifact, result, "")
}

func recordResultWithInventory(name, artifact string, result Result, inventory string) (returnErr error) {
	index := executionIndex{Version: 1, Entries: make(map[string]indexEntry)}
	file, err := os.Open(name)
	if err == nil {
		var loaded executionIndex
		decodeErr := decodeStrict(file, &loaded)
		closeErr := file.Close()
		if err := errors.Join(decodeErr, closeErr); err != nil {
			return err
		}
		if loaded.Version != 1 || loaded.Entries == nil {
			return errors.New("invalid execution index; preserve it for diagnosis")
		}
		index = loaded
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	abs, err := filepath.Abs(artifact)
	if err != nil {
		return err
	}
	index.Entries[result.Task+"/"+result.Role] = indexEntry{Artifact: abs, Status: result.Status, Source: result.Source}
	if inventory != "" {
		file, err := os.Open(inventory)
		if err != nil {
			return err
		}
		var session Session
		decodeErr := decodeStrict(file, &session)
		closeErr := file.Close()
		if err := errors.Join(decodeErr, closeErr); err != nil {
			return err
		}
		if err := session.validate(); err != nil {
			return err
		}
		index.Inventory, err = filepath.Abs(inventory)
		if err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
		return err
	}
	file, err = os.CreateTemp(filepath.Dir(name), ".handoff-index-*")
	if err != nil {
		return err
	}
	temp := file.Name()
	defer func() {
		if err := os.Remove(temp); err != nil && !errors.Is(err, os.ErrNotExist) {
			returnErr = errors.Join(returnErr, err)
		}
	}()
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	writeErr := encoder.Encode(index)
	syncErr := file.Sync()
	closeErr := file.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return err
	}
	return os.Rename(temp, name)
}
