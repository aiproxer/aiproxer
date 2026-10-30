package config

import "strings"

// defaultServerListenAddress is the implicit bind when [Config.Server].Address is empty
// (see [applyDefaultServerListenAddress]).
const defaultServerListenAddress = "127.0.0.1:8080"

// applyDefaultServerListenAddress sets server.address to an explicit loopback host when empty.
// Mode-aware broad-bind rules are enforced in [validateAccessAuth], not here.
func applyDefaultServerListenAddress(cfg *Config) {
	if cfg == nil {
		return
	}
	if strings.TrimSpace(cfg.Server.Address) == "" {
		cfg.Server.Address = defaultServerListenAddress
	}
}
