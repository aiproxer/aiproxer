package bunstore

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/securesession/domain"
)

type sessionScanRow interface {
	Scan(dest ...any) error
}

func scanRecord(row sessionScanRow) (domain.Record, error) {
	var (
		sid, ownerID, ownerIssuer, ownerTenant string
		wsID, clientSID, agentDigest           string
		policyVer, effTreat, strictPol         string
		routeHint, redactProf, auditMode       string
		aLegID, lastActSrc                     string
		status, qReason, qEventID              string
		fpBlob                                 []byte
		te, re                                 int
		lastActUnix, createdUnix               int64
		quarantinedAtUnix                      int64
		usageIn, usageOut                      int64
		attemptCount                           int
		traceJ, outcomeJ, acctJ                string
	)
	err := row.Scan(
		&sid, &fpBlob,
		&ownerID, &ownerIssuer, &ownerTenant,
		&wsID, &clientSID, &agentDigest,
		&policyVer, &te, &effTreat, &strictPol,
		&routeHint, &redactProf, &auditMode,
		&aLegID, &re,
		&status, &quarantinedAtUnix, &qReason, &qEventID,
		&lastActUnix, &lastActSrc, &createdUnix,
		&usageIn, &usageOut, &attemptCount,
		&traceJ, &outcomeJ, &acctJ,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Record{}, domain.ErrSessionNotFound
	}
	if err != nil {
		return domain.Record{}, opErr("scan session", err)
	}
	if len(fpBlob) != len(domain.TokenFingerprint{}) {
		return domain.Record{}, fmt.Errorf("bunstore: bad fingerprint length %d", len(fpBlob))
	}
	var fp domain.TokenFingerprint
	copy(fp[:], fpBlob)
	rec := domain.Record{
		SessionID:         domain.SessionID(sid),
		ResumeFingerprint: fp,
		Owner:             domain.PrincipalRef{ID: ownerID, Issuer: ownerIssuer, Tenant: ownerTenant},
		Workspace:         domain.WorkspaceRef{ID: wsID},
		ClientHints:       domain.ClientHints{ClientSessionID: clientSID, AgentIdentityDigest: agentDigest},
		Policy: domain.PolicyMetadata{
			PolicyVersion:            policyVer,
			TranscriptEnabled:        te != 0,
			EffectiveTreatment:       effTreat,
			StricterPolicyResolution: strictPol,
			RouteHint:                routeHint,
			RedactionProfile:         redactProf,
			AuditMode:                auditMode,
		},
		ALegID:               aLegID,
		Status:               domain.SessionStatus(status),
		QuarantineReasonCode: qReason,
		QuarantineEventID:    qEventID,
		ResumeEligible:       re != 0,
		LastActivityAt:       time.Unix(0, lastActUnix),
		LastActivitySource:   domain.ActivitySource(lastActSrc),
		CreatedAt:            time.Unix(0, createdUnix),
	}
	if quarantinedAtUnix != 0 || domain.SessionStatus(status).IsQuarantined() {
		rec.QuarantinedAt = time.Unix(0, quarantinedAtUnix)
	}
	if err := json.Unmarshal([]byte(traceJ), &rec.LatestAttemptTrace); err != nil {
		return domain.Record{}, opErr("decode trace json", err)
	}
	if err := json.Unmarshal([]byte(outcomeJ), &rec.LatestAttemptOutcome); err != nil {
		return domain.Record{}, opErr("decode outcome json", err)
	}
	if err := json.Unmarshal([]byte(acctJ), &rec.LatestAttemptAccounting); err != nil {
		return domain.Record{}, opErr("decode accounting json", err)
	}
	_ = usageIn
	_ = usageOut
	_ = attemptCount
	return rec, nil
}
