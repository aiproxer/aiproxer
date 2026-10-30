package runtime

import "errors"

// errTerminalDecisionContinuationPublished tells the receive loop that the
// continuation transaction has taken ownership of the next receive. The
// original response_finished candidate must not be emitted after publication.
var errTerminalDecisionContinuationPublished = errors.New("runtime: terminal decision continuation published")
