package terminalwork

// AppendIntentOutcome is the definitive result of a production AppendIntent
// attempt. Inserted and Replay are mutually exclusive; neither is set when the
// call returns an ambiguous transport/store error.
type AppendIntentOutcome struct {
	// Inserted is true only when this call definitively created the Intent row.
	Inserted bool
	// Replay is true only when an existing exact SameIntentReplay was confirmed.
	Replay bool
}

// Definitive reports whether the outcome classifies the append unambiguously.
func (o AppendIntentOutcome) Definitive() bool {
	return o.Inserted != o.Replay && (o.Inserted || o.Replay)
}
