// Package identity holds the core-owned proxy identity policy model: upstream
// User-Agent / OpenRouter app attribution and downstream Server presentation.
//
// Connector outbound application and A-leg Server middleware are out of scope;
// this package owns typed config, defaults, validation, and backend override merge.
package identity
