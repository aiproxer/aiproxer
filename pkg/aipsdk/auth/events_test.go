package auth

import (
	"reflect"
	"strings"
	"testing"
)

// forbiddenEventFieldNameSubstrings blocks field name patterns that would suggest
// first-class storage of bare secrets in audit events (per requirements 8.x / 9.x / 13.x).
var forbiddenEventFieldNameSubstrings = []string{
	"Bearer", "bearer", "BEARER",
	"Secret", "secret", "SECRET",
	"Password", "password",
	"Authorization", "authorization",
	"OAuth", "oauth", "Oauth",
	"Resume", "resume",
	"Token", "token",
	"APIKey", "ApiKey", "apikey",
	"KeyMaterial", "Raw",
}

func eventFieldNameForbidden(name string) bool {
	for _, sub := range forbiddenEventFieldNameSubstrings {
		if strings.Contains(name, sub) {
			return true
		}
	}
	return false
}

func TestAuthDecisionEvent_fieldNamesNotSuggestingSecrets(t *testing.T) {
	t.Parallel()
	typ := reflect.TypeFor[AuthDecisionEvent]()
	for field := range typ.Fields() {
		name := field.Name
		if eventFieldNameForbidden(name) {
			t.Fatalf("field %q looks like a secret-bearing column name", name)
		}
	}
}

func TestSessionStartEvent_fieldNamesNotSuggestingSecrets(t *testing.T) {
	t.Parallel()
	typ := reflect.TypeFor[SessionStartEvent]()
	for field := range typ.Fields() {
		name := field.Name
		if eventFieldNameForbidden(name) {
			t.Fatalf("field %q looks like a secret-bearing column name", name)
		}
	}
}

// InboundCallMeta is the only DTO in this package that may carry live bearer or hint material.
// Event DTOs must be separate struct types to avoid representing secrets in audit events.
func TestEventTypesDistinctFromInbound(t *testing.T) {
	t.Parallel()
	inT := reflect.TypeFor[InboundCallMeta]()
	if inT == reflect.TypeFor[AuthDecisionEvent]() || inT == reflect.TypeFor[SessionStartEvent]() {
		t.Fatal("inbound and event DTOs must be distinct struct types")
	}
}

func TestAccessMode_stringValues(t *testing.T) {
	t.Parallel()
	if a, w := string(AccessSingleUser), "single_user"; a != w {
		t.Fatalf("AccessSingleUser: got %q", a)
	}
	if a, w := string(AccessMultiUser), "multi_user"; a != w {
		t.Fatalf("AccessMultiUser: got %q", a)
	}
}
