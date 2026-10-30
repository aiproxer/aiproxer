package usageevidence

// ValidateCallUsageRecord validates an unsealed call without canonicalizing it.
// Billing selection uses the same validation as record sealing.
func ValidateCallUsageRecord(record CallUsageRecord) error {
	return record.validate()
}

// ValidateCallLegUsageRecord validates an unsealed leg without preparing or
// canonicalizing its evidence envelope.
func ValidateCallLegUsageRecord(record CallLegUsageRecord) error {
	return record.validate()
}
