package backendplugin

import "strings"

// Validate reports whether configure may proceed.
func (r ConfigureRequest) Validate() error {
	if err := MustNegotiateBeforeConfigure(r.Negotiation); err != nil {
		return err
	}
	if strings.TrimSpace(r.InstanceID) == "" || strings.TrimSpace(r.FactoryKind) == "" {
		return ErrInvalidInvocation
	}
	if err := ValidateSize(uint64(len(r.ConfigYAML)), DefaultMaxConfigYAMLBytes); err != nil {
		return err
	}
	tp := TransportPolicy{
		DisableAutomaticRetries: r.RuntimePolicy.DisableTransportRetries,
		MaxMessageBytes:         r.RuntimePolicy.MaxRequestBytes,
		MaxStreamFrameBytes:     r.RuntimePolicy.MaxStreamFrameBytes,
	}
	if tp.MaxMessageBytes == 0 {
		tp.MaxMessageBytes = DefaultMaxMessageBytes
	}
	if tp.MaxStreamFrameBytes == 0 {
		tp.MaxStreamFrameBytes = DefaultMaxStreamFrameBytes
	}
	return tp.Validate()
}
