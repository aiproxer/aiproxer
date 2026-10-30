package app

import (
	"context"
	"errors"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/terminalwork"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

// QueryClass distinguishes supported operator terminal-work query shapes.
type QueryClass string

const (
	QueryClassPendingTerminalWork QueryClass = "pending_terminal_work"
	QueryClassFinancialProjection QueryClass = "financial_projection"
)

// WorkQuery is a bounded operator filter for terminal-work rows.
type WorkQuery struct {
	WorkID     string
	RequestID  string
	AttemptID  string
	ProviderID string
	Kind       sdk.WorkKind
	State      sdk.WorkState
	Class      QueryClass
	Limit      int
	Cursor     string
}

// WorkRow is an operator-safe terminal-work projection (no raw payload/content).
type WorkRow struct {
	WorkID     string
	SourceKey  string
	Kind       sdk.WorkKind
	State      sdk.WorkState
	ProviderID string
	ErrorCode  string
	Payload    []byte
}

// WorkPage is one bounded page of operator-safe rows.
type WorkPage struct {
	Rows   []WorkRow
	Cursor string
}

// QueryStore lists persisted terminal-work rows.
type QueryStore interface {
	List(ctx context.Context, q terminalwork.ListQuery) (terminalwork.ListPage, error)
}

// QueryService exposes bounded, privacy-safe terminal-work queries.
type QueryService struct {
	store QueryStore
}

// NewQueryService returns an operator query service.
func NewQueryService(store QueryStore) *QueryService {
	return &QueryService{store: store}
}

// List returns operator-safe rows or a bounded query rejection.
func (s *QueryService) List(ctx context.Context, q WorkQuery) (WorkPage, error) {
	if s == nil || s.store == nil {
		return WorkPage{}, ErrNilIntentStore
	}
	if err := validateWorkQuery(q); err != nil {
		return WorkPage{}, err
	}
	lq := toListQuery(q)
	if err := terminalwork.ValidateListQuery(lq); err != nil {
		if errors.Is(err, terminalwork.ErrQueryTooBroad) {
			return WorkPage{}, ErrQueryTooBroad
		}
		if errors.Is(err, terminalwork.ErrQueryLimitExceeded) {
			return WorkPage{}, err
		}
		return WorkPage{}, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	page, err := s.store.List(ctx, lq)
	if err != nil {
		if errors.Is(err, terminalwork.ErrQueryTooBroad) {
			return WorkPage{}, ErrQueryTooBroad
		}
		return WorkPage{}, err
	}
	rows := make([]WorkRow, 0, len(page.Records))
	for _, rec := range page.Records {
		rows = append(rows, WorkRow{
			WorkID:     rec.WorkID,
			SourceKey:  rec.SourceKey.String(),
			Kind:       rec.Kind,
			State:      rec.State,
			ProviderID: rec.ProviderID,
			ErrorCode:  rec.Error.Code,
			Payload:    nil,
		})
	}
	return WorkPage{Rows: rows, Cursor: page.Cursor}, nil
}

func validateWorkQuery(q WorkQuery) error {
	switch q.Class {
	case "":
		// ok when other selective bounds exist
	case QueryClassPendingTerminalWork:
		// supported
	case QueryClassFinancialProjection:
		return ErrQueryUnsupported
	default:
		if q.Class != "" {
			return ErrQueryUnsupported
		}
	}
	return nil
}

func toListQuery(q WorkQuery) terminalwork.ListQuery {
	lq := terminalwork.ListQuery{
		WorkID:     strings.TrimSpace(q.WorkID),
		RequestID:  strings.TrimSpace(q.RequestID),
		AttemptID:  strings.TrimSpace(q.AttemptID),
		ProviderID: strings.TrimSpace(q.ProviderID),
		Kind:       q.Kind,
		State:      q.State,
		Limit:      q.Limit,
		Cursor:     q.Cursor,
	}
	if q.Class == QueryClassPendingTerminalWork && q.State == "" {
		lq.States = []sdk.WorkState{
			sdk.WorkStateIntent,
			sdk.WorkStatePending,
			sdk.WorkStateRetry,
			sdk.WorkStateClaimed,
		}
	}
	return lq
}
