package adapter

import (
	"context"
	"fmt"
	"net"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	publichost "github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin/host"
)

// DialConfiguredSession negotiates and configures a GRPCSession over an already
// peer-authenticated connection. It is the default DialAndConfigure path for
// discovered host-backed factories.
func DialConfiguredSession(
	ctx context.Context,
	conn net.Conn,
	instanceID, factoryKind string,
	configYAML []byte,
	secrets backendplugin.SecretBundle,
	policy backendplugin.RuntimePolicy,
) (*GRPCSession, backendplugin.ResolvedProfile, error) {
	if conn == nil {
		return nil, backendplugin.ResolvedProfile{}, fmt.Errorf("adapter: nil conn")
	}
	sess, profile, err := publichost.DialConfiguredSession(ctx, conn, instanceID, factoryKind, configYAML, secrets, policy)
	if err != nil {
		return nil, backendplugin.ResolvedProfile{}, fmt.Errorf("adapter: dial configured session: %w", err)
	}
	return sess, profile, nil
}
