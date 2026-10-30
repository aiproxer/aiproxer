package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"math/big"
	"os"
	"regexp"
	"strings"
)

var totalPattern = regexp.MustCompile(`^total:\s+\(statements\)\s+(\d+\.\d)%$`)

func parseTotal(input string) (*big.Rat, error) {
	var value string
	matched := 0
	scanner := bufio.NewScanner(strings.NewReader(input))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		matches := totalPattern.FindStringSubmatch(line)
		if len(matches) == 2 {
			matched++
			value = matches[1]
			continue
		}
		if strings.HasPrefix(line, "total:") {
			return nil, fmt.Errorf("malformed coverage total line %q", line)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if matched == 0 {
		return nil, errors.New("go tool cover output has no total line")
	}
	if matched != 1 {
		return nil, errors.New("go tool cover output has duplicate total lines")
	}
	result, ok := new(big.Rat).SetString(value)
	if !ok {
		return nil, fmt.Errorf("invalid coverage percentage %q", value)
	}
	return result, nil
}

func main() {
	funcFile := flag.String("func", "", "file containing go tool cover -func output")
	thresholdText := flag.String("threshold", "", "minimum percentage, for example 90")
	name := flag.String("name", "coverage", "coverage subject name")
	flag.Parse()
	if *funcFile == "" || *thresholdText == "" {
		fmt.Fprintln(os.Stderr, "usage: coverage-gate -func file -threshold percent [-name name]")
		os.Exit(2)
	}
	data, err := os.ReadFile(*funcFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	actual, err := parseTotal(string(data))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	threshold, ok := new(big.Rat).SetString(*thresholdText)
	if !ok {
		fmt.Fprintf(os.Stderr, "invalid threshold %q\n", *thresholdText)
		os.Exit(2)
	}
	percent := new(big.Rat).Mul(actual, big.NewRat(1, 1))
	fmt.Printf("%s: %s%% (threshold %s%%)\n", *name, percent.FloatString(2), threshold.FloatString(2))
	if actual.Cmp(threshold) < 0 {
		os.Exit(1)
	}
}
