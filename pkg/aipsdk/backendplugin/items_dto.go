package backendplugin

// ProtocolRequirements carries exact semantic/dialect/extension requirements on the plugin ABI.
type ProtocolRequirements struct {
	Capabilities       []string
	ItemDialects       []DialectRequirementDTO
	ReasoningDialects  []DialectRequirementDTO
	CompactionDialects []DialectRequirementDTO
	ExtensionTypes     []ExtensionRequirementDTO
}

// DialectRequirementDTO identifies an exact dialect requirement on the plugin ABI.
type DialectRequirementDTO struct {
	Kind        string
	Dialect     string
	Implementor string
}

// ExtensionRequirementDTO identifies a required extension type on the plugin ABI.
type ExtensionRequirementDTO struct {
	Namespace   string
	Type        string
	Implementor string
}

// InvocationItem is one ordered item on the plugin ABI.
type InvocationItem struct {
	Kind          string
	ID            string
	Status        string
	Role          Role
	Phase         string
	Content       []InvocationContentPart
	ToolCall      *InvocationToolCall
	ToolResult    *InvocationToolResult
	ItemReference *InvocationItemReference
	Reasoning     *InvocationReasoningItem
	Compaction    *InvocationCompactionItem
	Extension     *InvocationExtensionItem
}

// InvocationItemReference references a prior item by ID.
type InvocationItemReference struct {
	ID string
}

// InvocationReasoningItem carries reasoning payload on an ordered item.
type InvocationReasoningItem struct {
	Dialect   *string
	Text      *string
	Signature *string
	Opaque    RawJSON

	// Summary, Content, and EncryptedContent preserve exact OpenAI Responses
	// reasoning-item fields with absent/null/value presence (RawJSON states).
	// Additive v1.3 fields gated by FeatureExactOpenResponsesFields.
	Summary          RawJSON
	Content          RawJSON
	EncryptedContent RawJSON
}

// InvocationCompactionItem carries compaction payload on an ordered item.
type InvocationCompactionItem struct {
	EncapsulatedID string
	Dialect        string
	Implementor    string
	Opaque         RawJSON
	// EncryptedContent is the exact provider compaction blob (pinned profile
	// encrypted_content). Additive v1.3 field gated by FeatureExactOpenResponsesFields.
	EncryptedContent string
}

// InvocationExtensionItem carries opaque extension payload on an ordered item.
type InvocationExtensionItem struct {
	Namespace   string
	Type        string
	Implementor string
	Direction   string
	Opaque      RawJSON
}

// InvocationContentPart is one content fragment within an invocation item.
type InvocationContentPart struct {
	Kind           PartKind
	Text           *string
	ImageRef       *string
	ImageMIME      *string
	FileRef        *string
	FileMIME       *string
	FileName       *string
	VideoRef       *string
	VideoMIME      *string
	Reasoning      *InvocationReasoningPart
	Refusal        *string
	Summary        *string
	AnnotationType *string
	AnnotationData RawJSON
	AssistantRef   *string

	// FileData carries an inline base64 file payload (pinned profile file_data).
	// Additive v1.3 field gated by FeatureExactOpenResponsesFields.
	FileData *string

	// ExtensionType and ExtensionData preserve an opaque vendor-prefixed custom
	// content part (ContentPartExtension) losslessly. ExtensionType is the
	// prefixed wire discriminator; ExtensionData is the full raw part object.
	// Additive v1.3 fields gated by FeatureExactOpenResponsesFields.
	ExtensionType *string
	ExtensionData RawJSON
	// ExtensionNamespace and ExtensionImplementor preserve canonical extension
	// identity metadata independently of the derived Type namespace.
	ExtensionNamespace   *string
	ExtensionImplementor *string
}

// InvocationReasoningPart carries reasoning payload on the plugin ABI.
type InvocationReasoningPart struct {
	Dialect   *string
	Text      *string
	Signature *string
	Opaque    RawJSON

	// Summary, Content, and EncryptedContent preserve exact OpenAI Responses
	// reasoning-item fields with absent/null/value presence (RawJSON states).
	// Additive v1.3 fields gated by FeatureExactOpenResponsesFields.
	Summary          RawJSON
	Content          RawJSON
	EncryptedContent RawJSON
}

// InvocationToolCall carries tool call payload on the plugin ABI.
type InvocationToolCall struct {
	CallID    string
	Name      string
	Arguments RawJSON
}

// InvocationToolResult carries tool result payload on the plugin ABI.
type InvocationToolResult struct {
	CallID          string
	Name            string
	Output          *string
	StructuredParts []InvocationContentPart
}
