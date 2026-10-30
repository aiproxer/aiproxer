package runtime

import (
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/affinity"
	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/internal/core/routing"
)

func (e *Executor) resolveAffinityKey(sel *routing.Selector, views execctx.Views, viewsOK bool) (affinity.Key, bool, error) {
	if sel == nil || sel.Affinity == routing.AffinityNone {
		return affinity.Key{}, false, nil
	}
	if !viewsOK {
		return affinity.ResolveKey(affinity.IdentityInput{Mode: sel.Affinity, MissingIdentityPolicy: e.AffinityMissingIdentity})
	}
	if sel.Affinity == routing.AffinitySession && strings.TrimSpace(views.Session.AuthoritativeSessionID) == "" {
		views.Session.ClientSessionHint = ""
	}
	if sel.Affinity == routing.AffinitySession && strings.TrimSpace(views.Session.AuthoritativeSessionID) == "" && strings.TrimSpace(views.Session.ClientSessionHint) == "" {
		return affinity.ResolveKey(affinity.IdentityInput{Mode: sel.Affinity, MissingIdentityPolicy: e.AffinityMissingIdentity})
	}
	return affinity.ResolveKey(affinity.IdentityInput{
		Mode:                  sel.Affinity,
		Session:               views.Session,
		Principal:             views.Principal,
		MissingIdentityPolicy: e.AffinityMissingIdentity,
	})
}
