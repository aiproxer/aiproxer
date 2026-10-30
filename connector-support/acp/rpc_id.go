package acp

import (
	"encoding/json"
	"strconv"
)

// jsonRPCNumericID returns a JSON-RPC "id" value as a JSON number (no string quotes).
func jsonRPCNumericID(id int64) json.RawMessage {
	return json.RawMessage(strconv.AppendInt(nil, id, 10))
}
