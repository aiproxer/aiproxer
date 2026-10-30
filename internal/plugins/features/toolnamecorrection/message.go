package toolnamecorrection

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
)

// The fixed sentence of the hidden correction message: the sanitized invalid
// name, the offered tool names in catalog order, and - when they do not fit -
// the omitted count (requirements.md 4.1, 4.2).
const (
	// messageNameRunes bounds the sanitized invalid name.
	messageNameRunes = 64

	messageHeadOpen  = `Tool "`
	messageHeadClose = `" does not exist. Available tools: `
	messageTail      = `. Call one of the available tools, or answer without calling a tool.`

	messageListSeparator = ", "
	messageOmittedPrefix = " … and "
	messageOmittedSuffix = " more"
)

// buildMessage renders the hidden correction instruction for one unknown tool
// name. The invalid name is sanitized with [sanitizeToolName]; every catalog
// name is listed exactly, encoded as a JSON string literal. Names are listed
// in catalog order without descriptions or schemas. When the offered list does
// not fit [toolcall.MaxSteerInstructionBytes], the widest suffix this catalog
// can need is held back while listing, so the message with the appended omitted
// count always stays within the bound and always passes
// [toolcall.SteerRequest.Validate].
func buildMessage(name string, catalog []aipapi.ToolDef) string {
	head := messageHeadOpen + sanitizeToolName(name) + messageHeadClose

	names := make([]string, 0, len(catalog))
	listedBytes := 0
	for _, tool := range catalog {
		encoded := encodeCatalogName(tool.Name)
		names = append(names, encoded)
		listedBytes += len(encoded)
	}
	if len(names) > 0 {
		listedBytes += len(messageListSeparator) * (len(names) - 1)
	}
	if len(head)+listedBytes+len(messageTail) <= toolcall.MaxSteerInstructionBytes {
		return head + strings.Join(names, messageListSeparator) + messageTail
	}

	// The omitted count is at most len(names), so reserving room for the widest
	// possible suffix keeps room for whatever count is finally appended.
	reserve := len(messageOmittedPrefix) + len(strconv.Itoa(len(names))) + len(messageOmittedSuffix)
	budget := toolcall.MaxSteerInstructionBytes - len(head) - len(messageTail) - reserve

	usedBytes := 0
	listed := 0
	for _, candidate := range names {
		size := len(candidate)
		if listed > 0 {
			size += len(messageListSeparator)
		}
		if usedBytes+size > budget {
			break
		}
		usedBytes += size
		listed++
	}
	return head + strings.Join(names[:listed], messageListSeparator) +
		messageOmittedPrefix + strconv.Itoa(len(names)-listed) + messageOmittedSuffix + messageTail
}

// encodeCatalogName renders a catalog name as a JSON string literal, so quotes,
// backslashes, newlines, and control characters are escaped and no byte of a
// canonical name is dropped or truncated. HTML escaping stays disabled so the
// listing carries no escape overhead for '<', '>', and '&'. An empty name
// renders as "".
func encodeCatalogName(name string) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(name); err != nil {
		return `""`
	}
	return strings.TrimSuffix(buf.String(), "\n")
}

// sanitizeToolName reduces the invented tool name to at most messageNameRunes
// printable ASCII characters: control bytes and non-ASCII runes are dropped,
// and the double quote and backslash are dropped so the name cannot break out
// of the quoted sentence. It applies to the invented name only; catalog names
// are listed exactly (see [encodeCatalogName]). An entirely hostile name
// sanitizes to "", which still renders a valid message.
func sanitizeToolName(name string) string {
	var b strings.Builder
	count := 0
	for _, r := range name {
		if count == messageNameRunes {
			break
		}
		if r < ' ' || r > '~' || r == '"' || r == '\\' {
			continue
		}
		b.WriteRune(r)
		count++
	}
	return b.String()
}
