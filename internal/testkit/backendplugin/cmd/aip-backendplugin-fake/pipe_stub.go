//go:build !windows

package main

import (
	"fmt"
	"net"
)

func dialNamedPipe(string) (net.Conn, error) {
	return nil, fmt.Errorf("named pipe client unsupported on this OS")
}
