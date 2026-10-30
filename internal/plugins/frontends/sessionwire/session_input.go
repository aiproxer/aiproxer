package sessionwire

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

// SessionInputOptions parameterizes SessionInput extraction for a frontend profile.
type SessionInputOptions struct {
	HeaderAliases      aipsdk.HTTPHeaders
	RejectBodyMetadata bool
	MaxFactBytes       int64
}

// HasSessionMetadata reports whether meta contains any AIProxer-controlled session keys.
func HasSessionMetadata(meta map[string]string) bool {
	if len(meta) == 0 {
		return false
	}
	return strings.TrimSpace(meta[MetaKeyAuthoritativeSessionID]) != "" ||
		strings.TrimSpace(meta[MetaKeyResumeToken]) != ""
}

// BuildSessionInput builds an exact bounded largebody.SessionInput from HTTP headers
// and body metadata, enforcing:
//   - header/body/session/resume/client-session precedence (headers win over body metadata);
//   - rejection of body-carried AIProxer session metadata when RejectBodyMetadata is true (Requirements 14.2, 17.5);
//   - sensitive resume token wrapping via largebody.SensitiveString (Requirements 14.1, 22);
//   - exact scalar bounds on all session fields under aipapi envelope caps and semantic-fact budget.
func BuildSessionInput(h http.Header, meta map[string]string, opts SessionInputOptions) (largebody.SessionInput, error) {
	hasBodySession := HasSessionMetadata(meta)
	if opts.RejectBodyMetadata && hasBodySession {
		return largebody.SessionInput{}, largebody.ErrBodySessionMetadataRejected
	}

	if !opts.RejectBodyMetadata && len(meta) > 0 {
		if err := ValidateMetadata(meta); err != nil {
			return largebody.SessionInput{}, fmt.Errorf("sessionwire: %w", err)
		}
	}

	aliases := opts.HeaderAliases.OrDefault()
	sessionID := aliases.SessionIDValue(h)
	resumeToken := aliases.ResumeTokenValue(h)
	aLegID := aliases.ALegIDValue(h)
	sessionHint := aliases.SessionHintValue(h)

	bodySessionID := ""
	bodyResumeToken := ""
	if len(meta) > 0 {
		bodySessionID = strings.TrimSpace(meta[MetaKeyAuthoritativeSessionID])
		bodyResumeToken = strings.TrimSpace(meta[MetaKeyResumeToken])
	}

	src := largebody.SessionInputSource{
		HeaderAuthoritativeSessionID: sessionID,
		HeaderResumeToken:            resumeToken,
		HeaderALegID:                 aLegID,
		HeaderClientSessionHint:      sessionHint,
		BodyAuthoritativeSessionID:   bodySessionID,
		BodyResumeToken:              bodyResumeToken,
		HasBodySessionMetadata:       hasBodySession,
		RejectBodyMetadata:           opts.RejectBodyMetadata,
	}

	maxFactBytes := opts.MaxFactBytes
	if maxFactBytes <= 0 {
		maxFactBytes = 256 * 1024 // default 256 KiB semantic fact budget
	}

	in, err := largebody.BuildSessionInput(src, maxFactBytes)
	if err != nil {
		return largebody.SessionInput{}, fmt.Errorf("sessionwire: %w", err)
	}
	return in, nil
}
