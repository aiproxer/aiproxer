//go:build !windows

package main

import (
	"fmt"
	"net"
)

func dialNamedPipe(name string) (net.Conn, error) {
	return nil, fmt.Errorf("named pipe channel unsupported on this platform: %s", name)
}
