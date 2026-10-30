package auth

import (
	"strconv"
	"strings"
)

// String returns a log-safe single-line description; bearer and session hints are redacted.
func (m InboundCallMeta) String() string {
	return "InboundCallMeta(TraceID=" + redactForLog(m.TraceID) +
		", Frontend=" + redactForLog(m.Frontend) +
		", Method=" + m.Method +
		", Path=" + m.Path +
		", ClientAddr=" + redactForLog(m.ClientAddr) +
		", AuthorizationBearer=" + redactSecretField(m.AuthorizationBearer) +
		", SessionHint=" + redactSecretField(m.SessionHint) + ")"
}

// GoString is an alias of [InboundCallMeta.String] so fmt %#v and representation helpers stay safe.
func (m InboundCallMeta) GoString() string { return m.String() }

func redactSecretField(s string) string {
	trim := strings.TrimSpace(s)
	if trim == "" {
		return ""
	}
	return "<redacted len=" + strconv.Itoa(len(s)) + ">"
}

// redactForLog masks non-secret optional fields; empty stays empty, non-empty is marked present without value.
func redactForLog(s string) string {
	trim := strings.TrimSpace(s)
	if trim == "" {
		return ""
	}
	return "<omitted len=" + strconv.Itoa(len(s)) + ">"
}
