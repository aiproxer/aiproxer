// Package sdkadapter bridges trusted SDK contracts to the authoritative
// conversation-view domain ports. It contains the narrow registrar adapter for
// never_backend tagging and the steering writer application service that resolves
// after_ingress_tail anchors at Put time and persists rendered payloads verbatim.
// Construction is explicit with narrow interfaces; no global locator or client
// frontend exposure is provided.
package sdkadapter
