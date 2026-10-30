package capsule

// Encode emits the canonical sealed envelope bytes and never emits an
// unsealed transport envelope by accident.
func Encode(e Envelope) ([]byte, error) {
	if err := e.Verify(); err != nil {
		return nil, err
	}
	return marshalCanonical(e)
}

// Decode parses, validates, and verifies one complete envelope.
func Decode(data []byte) (Envelope, error) { return Parse(data) }

// Verify parses and verifies a serialized capsule against an optional parent
// binding. An empty expectedBranch only checks the self-digest.
func Verify(data []byte, expectedBranch string) (Envelope, error) {
	e, err := Parse(data)
	if err != nil {
		return Envelope{}, err
	}
	if err := e.VerifyBranch(expectedBranch); err != nil {
		return Envelope{}, err
	}
	return e, nil
}
