package db

import (
	"fmt"
	"net/url"
	"strings"
)

// pgUnsupportedDSNParams lists libpq connection parameters that Bun pgdriver
// does not honor. pgdriver forwards unknown URL query parameters to the server
// as SET commands; these options are not server GUCs, so the connection fails
// with an opaque driver error. Neon and other managed providers emit
// channel_binding=require by default, so stripping it here lets operators paste
// a libpq DSN directly into config without manual editing. TLS behavior stays
// governed by sslmode. Add other pgdriver-incompatible params here as managed
// providers emit them; keep the list minimal so legitimate DSN params survive.
var pgUnsupportedDSNParams = []string{"channel_binding"}

var pgSessionAffineDSNParams = []string{"search_path"}

// SanitizePostgresDSN returns dsn with libpq-only query parameters that Bun
// pgdriver cannot apply removed. Non-URL DSNs are returned unchanged (pgdriver
// only accepts URL-style DSNs, so a non-URL DSN will fail later with a clear
// "invalid scheme" error). The function does not log or expose DSN secrets.
func SanitizePostgresDSN(dsn string) (string, error) {
	before, after, ok := strings.Cut(dsn, "?")
	if !ok {
		return dsn, nil
	}
	base, rawQuery := before, after
	vals, err := url.ParseQuery(rawQuery)
	if err != nil {
		return "", fmt.Errorf("db: parse postgres dsn query: %w", err)
	}
	removed := false
	for _, target := range pgUnsupportedDSNParams {
		for k := range vals {
			if strings.EqualFold(k, target) {
				vals.Del(k)
				removed = true
			}
		}
	}
	if !removed {
		return dsn, nil
	}
	encoded := vals.Encode()
	if encoded == "" {
		return base, nil
	}
	return base + "?" + encoded, nil
}

// ValidateTransactionPoolDSN rejects connection parameters whose behavior
// depends on preserving PostgreSQL session state across transactions.
func ValidateTransactionPoolDSN(dsn string) error {
	_, rawQuery, ok := strings.Cut(dsn, "?")
	if !ok {
		return nil
	}
	vals, err := url.ParseQuery(rawQuery)
	if err != nil {
		return fmt.Errorf("db: parse postgres dsn query: %w", err)
	}
	for _, target := range pgSessionAffineDSNParams {
		for key := range vals {
			if strings.EqualFold(key, target) {
				return fmt.Errorf("db: transaction-pool postgres dsn must not set %s", target)
			}
			if !strings.EqualFold(key, "options") {
				continue
			}
			for _, value := range vals[key] {
				if transactionPoolOptionsContainSessionAffineSetting(value, target) {
					return fmt.Errorf("db: transaction-pool postgres dsn must not set %s via options", target)
				}
			}
		}
	}
	return nil
}

func transactionPoolOptionsContainSessionAffineSetting(raw, target string) bool {
	target = strings.ToLower(strings.TrimSpace(target))
	if target == "" {
		return false
	}
	for token := range strings.FieldsSeq(strings.ToLower(raw)) {
		token = strings.Trim(token, `"'`)
		if token == "-c" {
			continue
		}
		token = strings.TrimPrefix(token, "-c")
		token = strings.TrimSpace(token)
		if token == target || strings.HasPrefix(token, target+"=") {
			return true
		}
	}
	return false
}
