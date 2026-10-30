package geoip

import "time"

const (
	MaxForwardedHeaderBytes  = 16 << 10
	MaxForwardedHops         = 32
	MaxDatabaseDownloadBytes = 128 << 20
	DatabaseOperationTimeout = 2 * time.Minute
	DefaultUpdateInterval    = 24 * time.Hour
	MinUpdateInterval        = 6 * time.Hour
	MaxUpdateInterval        = 168 * time.Hour
	UpdateJitterFraction     = 0.10
)
