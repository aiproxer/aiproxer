package metering

import "errors"

const MaxQueryLimit = 500

// ErrQueryTooBroad is returned when List lacks a required selective bound so
// the store cannot safely page without scanning (requirements 14.4, 14.8).
var ErrQueryTooBroad = errors.New("metering: query too broad")

// ErrQueryUnsupported is returned when the query class or filter shape is not
// supported by the metering journal querier (requirements 14.5, 14.8).
var ErrQueryUnsupported = errors.New("metering: query unsupported")

var ErrQueryLimitExceeded = errors.New("metering: query limit exceeded")
