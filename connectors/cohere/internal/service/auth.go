package service

import (
	"context"
)

type TokenProvider interface {
	Token(ctx context.Context) (string, error)
}

type StaticTokenProvider string

func (s StaticTokenProvider) Token(context.Context) (string, error) {
	return string(s), nil
}
