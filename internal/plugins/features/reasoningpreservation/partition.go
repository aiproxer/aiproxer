package reasoningpreservation

import "strings"

func sessionPartitionOrMiss(authoritativeID string) (SessionPartition, bool) {
	id := strings.TrimSpace(authoritativeID)
	if id == "" {
		return SessionPartition{}, false
	}
	return NewSessionPartition(id), true
}
