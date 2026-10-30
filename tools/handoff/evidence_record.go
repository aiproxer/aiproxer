package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
)

func logDigest(name string) (string, error) {
	file, err := os.Open(name)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	_, readErr := io.Copy(hash, file)
	return hex.EncodeToString(hash.Sum(nil)), errors.Join(readErr, file.Close())
}

func hasGoTestFailure(name string) (bool, error) {
	file, err := os.Open(name)
	if err != nil {
		return false, err
	}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), 2<<20)
	found := false
	for scanner.Scan() {
		if goTestAssertionFailure(scanner.Bytes()) {
			found = true
			break
		}
	}
	return found, errors.Join(scanner.Err(), file.Close())
}

// commandRecord binds the command and source to the retained output. It is
// integrity evidence, not an authenticated attestation or a review verdict.
type commandRecord struct {
	Source    SourceState `json:"source"`
	Command   Command     `json:"command"`
	LogSHA256 string      `json:"log_sha256"`
}

func goTestAssertionFailure(data []byte) bool {
	for line := range bytes.SplitSeq(data, []byte("\n")) {
		if bytes.HasPrefix(bytes.TrimSpace(line), []byte("--- FAIL: Test")) || bytes.HasPrefix(bytes.TrimSpace(line), []byte("--- FAIL: Fuzz")) {
			return true
		}
		var event struct{ Action, Test string }
		if json.Unmarshal(line, &event) == nil && event.Action == "fail" && event.Test != "" {
			return true
		}
	}
	return false
}

func writeJSONAtomic(name string, value any) (err error) {
	file, err := os.CreateTemp(filepath.Dir(name), ".handoff-*")
	if err != nil {
		return err
	}
	temp := file.Name()
	defer func() {
		if removeErr := os.Remove(temp); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			err = errors.Join(err, removeErr)
		}
	}()
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	writeErr := encoder.Encode(value)
	err = errors.Join(writeErr, file.Sync(), file.Close())
	if err != nil {
		return err
	}
	return os.Rename(temp, name)
}

func loadCommandRecord(name string) (commandRecord, error) {
	file, err := os.Open(name)
	if err != nil {
		return commandRecord{}, err
	}
	var record commandRecord
	err = decodeStrict(file, &record)
	return record, errors.Join(err, file.Close())
}

func checkCommandRecord(name string, command Command, source SourceState) error {
	var err error
	name, err = filepath.Abs(name)
	if err != nil {
		return err
	}
	receipt := name + ".record.json"
	if command.Record != "" {
		receipt = command.Record
	}
	record, err := loadCommandRecord(receipt)
	if errors.Is(err, os.ErrNotExist) && command.Record == "" {
		return nil
	} // Legacy manual logs remain readable.
	if err != nil {
		return err
	}
	if record.Command.Purpose != command.Purpose || !slices.Equal(record.Command.Argv, command.Argv) || record.Command.ExitCode == nil || *record.Command.ExitCode != *command.ExitCode || filepath.Clean(record.Command.Evidence) != filepath.Clean(name) {
		return errors.New("command differs from recorded evidence")
	}
	digest, err := logDigest(name)
	if err != nil {
		return err
	}
	if record.LogSHA256 != digest {
		return errors.New("evidence log changed since capture")
	}
	if command.Purpose == "verification" && record.Source != source {
		return errors.New("verification belongs to a different source; recapture or classify as baseline")
	}
	if command.Purpose == "red" && len(command.Argv) > 1 && filepath.Base(command.Argv[0]) == "go" && command.Argv[1] == "test" {
		failed, err := hasGoTestFailure(name)
		if err != nil {
			return err
		}
		if !failed {
			return errors.New("go test RED requires a failing test, not only a build or infrastructure failure")
		}
	}
	return nil
}

func assembleResult(name string, source SourceState, records []string) error {
	file, err := os.Open(name)
	if err != nil {
		return err
	}
	result, decodeErr := decodeResult(file)
	if err := errors.Join(decodeErr, file.Close()); err != nil {
		return err
	}
	result.Source, result.Commands = source, []Command{}
	for _, name := range records {
		record, err := loadCommandRecord(name)
		if err != nil {
			return err
		}
		if name != record.Command.Evidence+".record.json" {
			return fmt.Errorf("record must accompany its evidence log: %s", name)
		}
		record.Command.Record = name
		result.Commands = append(result.Commands, record.Command)
	}
	if err := result.Validate(filepath.Dir(name)); err != nil {
		return err
	}
	return writeJSONAtomic(name, result)
}
