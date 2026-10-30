package checkcfg

import (
	"fmt"
	"strings"
)

func RequireNonEmpty(backendID, field, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s: %s is required", backendID, field)
	}
	return nil
}
