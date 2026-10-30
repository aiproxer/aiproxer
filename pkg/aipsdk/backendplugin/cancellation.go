package backendplugin

import "slices"

// CancellationHandshakeNegotiated reports whether the cancellation handshake feature was negotiated.
func CancellationHandshakeNegotiated(neg Negotiation) bool {
	return neg.Compatible && neg.NegotiatedMinor >= ProtocolMinorCancellationHandshake && slices.Contains(neg.EnabledFeatures, FeatureCancellationHandshake)
}
