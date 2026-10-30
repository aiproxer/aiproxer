//go:build !windows

package backendplugin

import (
	"fmt"
	"net"
)

func dialNamedPipe(name string) (net.Conn, error) {
	return nil, fmt.Errorf("named pipe channel unsupported on this platform: %s", name)
}
