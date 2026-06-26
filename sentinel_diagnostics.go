package sentinel

import "sync/atomic"

// SentinelDiagnostics captures aggregate counters for Sentinel challenge handling.
// It intentionally stores counts only; no tokens, cookies, request bodies, or user prompts.
type SentinelDiagnostics struct {
	TurnstileAdvertised       uint64 `json:"turnstile_advertised"`
	TurnstileFinalizeAccepted uint64 `json:"turnstile_finalize_accepted"`
	TurnstileFinalizeRejected uint64 `json:"turnstile_finalize_rejected"`
	PoWRequired               uint64 `json:"pow_required"`
	PoWSolved                 uint64 `json:"pow_solved"`
	PoWFailed                 uint64 `json:"pow_failed"`
}

var sentinelDiagnostics struct {
	turnstileAdvertised       atomic.Uint64
	turnstileFinalizeAccepted atomic.Uint64
	turnstileFinalizeRejected atomic.Uint64
	powRequired               atomic.Uint64
	powSolved                 atomic.Uint64
	powFailed                 atomic.Uint64
}

// GetSentinelDiagnostics returns process-local aggregate Sentinel diagnostics.
func GetSentinelDiagnostics() SentinelDiagnostics {
	return SentinelDiagnostics{
		TurnstileAdvertised:       sentinelDiagnostics.turnstileAdvertised.Load(),
		TurnstileFinalizeAccepted: sentinelDiagnostics.turnstileFinalizeAccepted.Load(),
		TurnstileFinalizeRejected: sentinelDiagnostics.turnstileFinalizeRejected.Load(),
		PoWRequired:               sentinelDiagnostics.powRequired.Load(),
		PoWSolved:                 sentinelDiagnostics.powSolved.Load(),
		PoWFailed:                 sentinelDiagnostics.powFailed.Load(),
	}
}
