package main

import (
	"crypto/sha256"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func main() {
	mode := flag.String("mode", "success", "helper mode")
	exit := flag.Int("exit", 1, "exit status")
	key := flag.String("key", "", "environment key")
	size := flag.Int("size", 0, "output size")
	text := flag.String("text", "", "text to print")
	marker := flag.String("marker", "", "failure marker")
	readyFile := flag.String("ready-file", "", "grandchild ready marker")
	pidFile := flag.String("pid-file", "", "grandchild pid marker")
	ioFile := flag.String("io-file", "", "accounting I/O marker")
	detach := flag.Bool("detach", false, "start the pipe-holding grandchild in its own process group")
	exitAfterSpawn := flag.Bool("exit-after-spawn", false, "exit after starting the pipe-holding grandchild")
	flag.Parse()

	switch *mode {
	case "success":
		fmt.Println("success")
	case "fail":
		os.Exit(*exit)
	case "sleep":
		for {
			time.Sleep(time.Second)
		}
	case "print-cwd":
		cwd, err := os.Getwd()
		if err != nil {
			panic(err)
		}
		fmt.Println(cwd)
	case "print-env":
		fmt.Println(os.Getenv(*key))
	case "output":
		_, _ = fmt.Fprint(os.Stdout, strings.Repeat("O", *size/2)+strings.Repeat("o", *size-*size/2))
		_, _ = fmt.Fprint(os.Stderr, strings.Repeat("E", *size/2)+strings.Repeat("e", *size-*size/2))
	case "print":
		fmt.Print(*text)
	case "fail-output":
		_, _ = fmt.Fprint(os.Stdout, *marker)
		fmt.Fprint(os.Stderr, "child stderr")
		os.Exit(*exit)
	case "grandchild":
		if *readyFile != "" {
			if err := os.WriteFile(*readyFile, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
				panic(err)
			}
		}
		for {
			time.Sleep(time.Second)
		}
	case "spawn-grandchild":
		self, err := os.Executable()
		if err != nil {
			panic(err)
		}
		child := exec.Command(self, "-mode=grandchild", "-ready-file", *readyFile)
		if err := child.Start(); err != nil {
			panic(err)
		}
		if *pidFile != "" {
			if err := os.WriteFile(*pidFile, []byte(strconv.FormatInt(int64(child.Process.Pid), 10)), 0o600); err != nil {
				panic(err)
			}
		}
		if *exitAfterSpawn {
			return
		}
		for {
			time.Sleep(time.Second)
		}
	case "spawn-pipe-holder":
		self, err := os.Executable()
		if err != nil {
			panic(err)
		}
		child := exec.Command(self, "-mode=grandchild")
		// Hand this process's stream pipes to the grandchild. With nil stdout
		// and stderr, os/exec would point the grandchild at the null device
		// instead of letting it keep the pipe open.
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if *detach {
			attr, ok := detachedProcessGroupAttr()
			if !ok {
				panic("detach is not supported on this platform")
			}
			child.SysProcAttr = attr
		}
		if err := child.Start(); err != nil {
			panic(err)
		}
		if *pidFile != "" {
			if err := writePIDFile(*pidFile, child.Process.Pid); err != nil {
				panic(err)
			}
		}
		if *exitAfterSpawn {
			return
		}
		for {
			time.Sleep(time.Second)
		}
	case "accounting-tree":
		self, err := os.Executable()
		if err != nil {
			panic(err)
		}
		child := exec.Command(self, "-mode=accounting-child", "-io-file", *ioFile)
		if output, err := child.CombinedOutput(); err != nil {
			panic(fmt.Errorf("accounting child: %w: %s", err, output))
		}
	case "accounting-child":
		var digest [32]byte
		block := make([]byte, 64*1024)
		for range 4096 {
			digest = sha256.Sum256(append(block, digest[:]...))
		}
		if *ioFile == "" {
			panic("accounting-child requires -io-file")
		}
		if err := os.WriteFile(*ioFile, digest[:], 0o600); err != nil {
			panic(err)
		}
	default:
		panic("unknown mode")
	}
}

// writePIDFile publishes pid atomically: it writes a sibling temporary file and
// renames it into place, so a reader polling the path never observes an empty
// or partially written pid. The new fixture still retries incomplete reads; the
// atomic rename removes the ordinary cause of them.
func writePIDFile(path string, pid int) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.WriteString(strconv.Itoa(pid)); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
		return err
	}
	return nil
}
