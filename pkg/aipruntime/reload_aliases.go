package aipruntime

import (
	sdkreload "github.com/aiproxer/aiproxer/pkg/aipsdk/configreload"
)

// Public reload names alias pkg/aipsdk/configreload (Req 7.4); no mirrored domain copy.

type (
	TriggerKind    = sdkreload.TriggerKind
	ResultCategory = sdkreload.ResultCategory
	ReloadTrigger  = sdkreload.Trigger
	ReloadResult   = sdkreload.Result
	HistoryEntry   = sdkreload.HistoryEntry
	ReloadStatus   = sdkreload.Status
)

const (
	TriggerSIGHUP = sdkreload.TriggerSIGHUP
	TriggerAPI    = sdkreload.TriggerAPI

	ResultPublished         = sdkreload.ResultPublished
	ResultNoop              = sdkreload.ResultNoop
	ResultBusy              = sdkreload.ResultBusy
	ResultRestartRequired   = sdkreload.ResultRestartRequired
	ResultRetentionBlocked  = sdkreload.ResultRetentionBlocked
	ResultInvalid           = sdkreload.ResultInvalid
	ResultSourceIntegrity   = sdkreload.ResultSourceIntegrity
	ResultCanceled          = sdkreload.ResultCanceled
	ResultPreparationFailed = sdkreload.ResultPreparationFailed
	ResultInternalFailed    = sdkreload.ResultInternalFailed
)

// ResultCategories re-exports the canonical closed-vocabulary accessor; each call
// returns a defensive copy, so callers cannot mutate shared enumeration state.
var ResultCategories = sdkreload.ResultCategories
