//go:build windows

package main

import (
	"fmt"
	"net"
	"time"

	"github.com/Microsoft/go-winio"
)

func dialNamedPipe(name string) (net.Conn, error) {
	d := 5 * time.Second
	c, err := winio.DialPipe(name, &d)
	if err != nil {
		return nil, fmt.Errorf("dial pipe %q: %w", name, err)
	}
	return c, nil
}
