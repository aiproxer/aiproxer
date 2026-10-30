package backendplugin

import backendpluginv1 "github.com/aiproxer/aiproxer/api/backendplugin/v1"

func invocationWireFromProto(p *backendpluginv1.Invocation, inv *Invocation) error {
	if p == nil || inv == nil {
		return nil
	}
	inv.Operation = p.GetOperation()
	inv.DeliveryMode = p.GetDeliveryMode()
	inv.TransportMode = p.GetTransportMode()
	inv.ItemAuthority = p.GetItemAuthority()
	inv.PromptCacheKey = p.GetPromptCacheKey()
	for _, item := range p.GetItems() {
		mapped, err := invocationItemFromProto(item)
		if err != nil {
			return err
		}
		inv.Items = append(inv.Items, mapped)
	}
	if req := p.GetProtocolRequirements(); req != nil {
		inv.ProtocolRequirements = protocolRequirementsFromProto(req)
	}
	return nil
}

func invocationWireToProto(inv Invocation, out *backendpluginv1.Invocation) error {
	if out == nil {
		return nil
	}
	out.Operation = inv.Operation
	out.DeliveryMode = inv.DeliveryMode
	out.TransportMode = inv.TransportMode
	out.ItemAuthority = inv.ItemAuthority
	out.PromptCacheKey = inv.PromptCacheKey
	for _, item := range inv.Items {
		pi, err := invocationItemToProto(item)
		if err != nil {
			return err
		}
		out.Items = append(out.Items, pi)
	}
	if inv.ItemAuthority {
		out.ProtocolRequirements = protocolRequirementsToProto(inv.ProtocolRequirements)
	}
	return nil
}

func protocolRequirementsFromProto(p *backendpluginv1.ProtocolRequirementsWire) ProtocolRequirements {
	out := ProtocolRequirements{Capabilities: append([]string(nil), p.GetCapabilities()...)}
	for _, d := range p.GetItemDialects() {
		out.ItemDialects = append(out.ItemDialects, dialectRequirementFromProto(d))
	}
	for _, d := range p.GetReasoningDialects() {
		out.ReasoningDialects = append(out.ReasoningDialects, dialectRequirementFromProto(d))
	}
	for _, d := range p.GetCompactionDialects() {
		out.CompactionDialects = append(out.CompactionDialects, dialectRequirementFromProto(d))
	}
	for _, e := range p.GetExtensionTypes() {
		out.ExtensionTypes = append(out.ExtensionTypes, extensionRequirementFromProto(e))
	}
	return out
}

func protocolRequirementsToProto(dto ProtocolRequirements) *backendpluginv1.ProtocolRequirementsWire {
	out := &backendpluginv1.ProtocolRequirementsWire{Capabilities: append([]string(nil), dto.Capabilities...)}
	for _, d := range dto.ItemDialects {
		out.ItemDialects = append(out.ItemDialects, dialectRequirementToProto(d))
	}
	for _, d := range dto.ReasoningDialects {
		out.ReasoningDialects = append(out.ReasoningDialects, dialectRequirementToProto(d))
	}
	for _, d := range dto.CompactionDialects {
		out.CompactionDialects = append(out.CompactionDialects, dialectRequirementToProto(d))
	}
	for _, e := range dto.ExtensionTypes {
		out.ExtensionTypes = append(out.ExtensionTypes, extensionRequirementToProto(e))
	}
	return out
}

func dialectRequirementFromProto(p *backendpluginv1.DialectRequirementWire) DialectRequirementDTO {
	if p == nil {
		return DialectRequirementDTO{}
	}
	return DialectRequirementDTO{Kind: p.GetKind(), Dialect: p.GetDialect(), Implementor: p.GetImplementor()}
}

func dialectRequirementToProto(d DialectRequirementDTO) *backendpluginv1.DialectRequirementWire {
	return &backendpluginv1.DialectRequirementWire{Kind: d.Kind, Dialect: d.Dialect, Implementor: d.Implementor}
}

func extensionRequirementFromProto(p *backendpluginv1.ExtensionRequirementWire) ExtensionRequirementDTO {
	if p == nil {
		return ExtensionRequirementDTO{}
	}
	return ExtensionRequirementDTO{Namespace: p.GetNamespace(), Type: p.GetType(), Implementor: p.GetImplementor()}
}

func extensionRequirementToProto(e ExtensionRequirementDTO) *backendpluginv1.ExtensionRequirementWire {
	return &backendpluginv1.ExtensionRequirementWire{Namespace: e.Namespace, Type: e.Type, Implementor: e.Implementor}
}

func invocationItemFromProto(p *backendpluginv1.InvocationItem) (InvocationItem, error) {
	if p == nil {
		return InvocationItem{}, ErrInvalidInvocation
	}
	item := InvocationItem{
		Kind:   p.GetKind(),
		ID:     p.GetId(),
		Status: p.GetStatus(),
		Phase:  p.GetPhase(),
	}
	if p.GetRole() != backendpluginv1.Role_ROLE_UNSPECIFIED {
		role, err := roleFromProto(p.GetRole())
		if err != nil {
			return InvocationItem{}, err
		}
		item.Role = role
	}
	for _, cp := range p.GetContent() {
		part, err := invocationContentPartFromProto(cp)
		if err != nil {
			return InvocationItem{}, err
		}
		item.Content = append(item.Content, part)
	}
	if tc := p.GetToolCall(); tc != nil {
		args, err := RawJSONFromProto(tc.GetArguments())
		if err != nil {
			return InvocationItem{}, err
		}
		item.ToolCall = &InvocationToolCall{
			CallID: tc.GetCallId(), Name: tc.GetName(), Arguments: args,
		}
	}
	if tr := p.GetToolResult(); tr != nil {
		res := &InvocationToolResult{CallID: tr.GetCallId(), Name: tr.GetName()}
		if tr.Output != nil {
			v := tr.GetOutput()
			res.Output = &v
		}
		for _, cp := range tr.GetStructuredParts() {
			part, err := invocationContentPartFromProto(cp)
			if err != nil {
				return InvocationItem{}, err
			}
			res.StructuredParts = append(res.StructuredParts, part)
		}
		item.ToolResult = res
	}
	if ref := p.GetItemReference(); ref != nil {
		item.ItemReference = &InvocationItemReference{ID: ref.GetId()}
	}
	if r := p.GetReasoning(); r != nil {
		reasoning, err := invocationReasoningItemFromProto(r)
		if err != nil {
			return InvocationItem{}, err
		}
		item.Reasoning = reasoning
	}
	if c := p.GetCompaction(); c != nil {
		opaque, err := RawJSONFromProto(c.GetOpaque())
		if err != nil {
			return InvocationItem{}, err
		}
		item.Compaction = &InvocationCompactionItem{
			EncapsulatedID:   c.GetEncapsulatedId(),
			Dialect:          c.GetDialect(),
			Implementor:      c.GetImplementor(),
			Opaque:           opaque,
			EncryptedContent: c.GetEncryptedContent(),
		}
	}
	if ext := p.GetExtension(); ext != nil {
		opaque, err := RawJSONFromProto(ext.GetOpaque())
		if err != nil {
			return InvocationItem{}, err
		}
		item.Extension = &InvocationExtensionItem{
			Namespace: ext.GetNamespace(), Type: ext.GetType(), Implementor: ext.GetImplementor(),
			Direction: ext.GetDirection(), Opaque: opaque,
		}
	}
	return item, nil
}

func invocationItemToProto(item InvocationItem) (*backendpluginv1.InvocationItem, error) {
	out := &backendpluginv1.InvocationItem{
		Kind: item.Kind, Id: item.ID, Status: item.Status, Phase: item.Phase,
	}
	if item.Role != "" {
		role, err := roleToProto(item.Role)
		if err != nil {
			return nil, err
		}
		out.Role = role
	}
	for _, cp := range item.Content {
		pcp, err := invocationContentPartToProto(cp)
		if err != nil {
			return nil, err
		}
		out.Content = append(out.Content, pcp)
	}
	if item.ToolCall != nil {
		out.ToolCall = &backendpluginv1.InvocationToolCall{
			CallId: item.ToolCall.CallID, Name: item.ToolCall.Name, Arguments: RawJSONToProto(item.ToolCall.Arguments),
		}
	}
	if item.ToolResult != nil {
		out.ToolResult = &backendpluginv1.InvocationToolResult{CallId: item.ToolResult.CallID, Name: item.ToolResult.Name}
		if item.ToolResult.Output != nil {
			out.ToolResult.Output = item.ToolResult.Output
		}
		for _, cp := range item.ToolResult.StructuredParts {
			pcp, err := invocationContentPartToProto(cp)
			if err != nil {
				return nil, err
			}
			out.ToolResult.StructuredParts = append(out.ToolResult.StructuredParts, pcp)
		}
	}
	if item.ItemReference != nil {
		out.ItemReference = &backendpluginv1.InvocationItemReference{Id: item.ItemReference.ID}
	}
	if item.Reasoning != nil {
		out.Reasoning = invocationReasoningItemToProto(item.Reasoning)
	}
	if item.Compaction != nil {
		out.Compaction = &backendpluginv1.InvocationCompactionItem{
			Dialect:     item.Compaction.Dialect,
			Implementor: item.Compaction.Implementor,
			Opaque:      RawJSONToProto(item.Compaction.Opaque),
		}
		if item.Compaction.EncapsulatedID != "" {
			out.Compaction.EncapsulatedId = &item.Compaction.EncapsulatedID
		}
		if item.Compaction.EncryptedContent != "" {
			ec := item.Compaction.EncryptedContent
			out.Compaction.EncryptedContent = &ec
		}
	}
	if item.Extension != nil {
		out.Extension = &backendpluginv1.InvocationExtensionItem{
			Namespace: item.Extension.Namespace, Type: item.Extension.Type, Implementor: item.Extension.Implementor,
			Opaque: RawJSONToProto(item.Extension.Opaque),
		}
		if item.Extension.Direction != "" {
			out.Extension.Direction = &item.Extension.Direction
		}
	}
	return out, nil
}

func invocationReasoningItemFromProto(p *backendpluginv1.InvocationReasoningItem) (*InvocationReasoningItem, error) {
	if p == nil {
		return nil, nil
	}
	out := &InvocationReasoningItem{}
	if p.Dialect != nil {
		v := p.GetDialect()
		out.Dialect = &v
	}
	if p.Text != nil {
		v := p.GetText()
		out.Text = &v
	}
	if p.Signature != nil {
		v := p.GetSignature()
		out.Signature = &v
	}
	if raw := p.GetOpaque(); raw != nil {
		opaque, err := RawJSONFromProto(raw)
		if err != nil {
			return nil, err
		}
		out.Opaque = opaque
	}
	for _, r := range []struct {
		raw  *backendpluginv1.RawJSONValue
		dest *RawJSON
	}{{p.Summary, &out.Summary}, {p.Content, &out.Content}, {p.EncryptedContent, &out.EncryptedContent}} {
		if r.raw == nil {
			continue
		}
		val, err := RawJSONFromProto(r.raw)
		if err != nil {
			return nil, err
		}
		*r.dest = val
	}
	return out, nil
}

func invocationReasoningItemToProto(r *InvocationReasoningItem) *backendpluginv1.InvocationReasoningItem {
	if r == nil {
		return nil
	}
	out := &backendpluginv1.InvocationReasoningItem{}
	if r.Dialect != nil {
		out.Dialect = r.Dialect
	}
	if r.Text != nil {
		out.Text = r.Text
	}
	if r.Signature != nil {
		out.Signature = r.Signature
	}
	if r.Opaque.State() == RawJSONValue {
		out.Opaque = RawJSONToProto(r.Opaque)
	}
	if r.Summary.State() == RawJSONValue {
		out.Summary = RawJSONToProto(r.Summary)
	}
	if r.Content.State() == RawJSONValue {
		out.Content = RawJSONToProto(r.Content)
	}
	if r.EncryptedContent.State() != RawJSONAbsent {
		out.EncryptedContent = RawJSONToProto(r.EncryptedContent)
	}
	return out
}

func invocationContentPartFromProto(p *backendpluginv1.InvocationContentPart) (InvocationContentPart, error) {
	if p == nil {
		return InvocationContentPart{}, ErrInvalidInvocation
	}
	part := InvocationContentPart{}
	kind, err := partKindFromProto(p.GetKind())
	if err != nil {
		return InvocationContentPart{}, err
	}
	part.Kind = kind
	if p.Text != nil {
		v := p.GetText()
		part.Text = &v
	}
	if p.ImageRef != nil {
		v := p.GetImageRef()
		part.ImageRef = &v
	}
	if p.ImageMime != nil {
		v := p.GetImageMime()
		part.ImageMIME = &v
	}
	if p.FileRef != nil {
		v := p.GetFileRef()
		part.FileRef = &v
	}
	if p.FileMime != nil {
		v := p.GetFileMime()
		part.FileMIME = &v
	}
	if p.FileName != nil {
		v := p.GetFileName()
		part.FileName = &v
	}
	if p.FileData != nil {
		v := p.GetFileData()
		part.FileData = &v
	}
	if p.VideoRef != nil {
		v := p.GetVideoRef()
		part.VideoRef = &v
	}
	if p.VideoMime != nil {
		v := p.GetVideoMime()
		part.VideoMIME = &v
	}
	if p.Refusal != nil {
		v := p.GetRefusal()
		part.Refusal = &v
	}
	if p.Summary != nil {
		v := p.GetSummary()
		part.Summary = &v
	}
	if p.AssistantRef != nil {
		v := p.GetAssistantRef()
		part.AssistantRef = &v
	}
	if p.AnnotationType != nil {
		v := p.GetAnnotationType()
		part.AnnotationType = &v
	}
	if raw := p.GetAnnotationData(); raw != nil {
		data, err := RawJSONFromProto(raw)
		if err != nil {
			return InvocationContentPart{}, err
		}
		part.AnnotationData = data
	}
	if p.ExtensionType != nil {
		v := p.GetExtensionType()
		part.ExtensionType = &v
	}
	if p.ExtensionNamespace != nil {
		v := p.GetExtensionNamespace()
		part.ExtensionNamespace = &v
	}
	if p.ExtensionImplementor != nil {
		v := p.GetExtensionImplementor()
		part.ExtensionImplementor = &v
	}
	if raw := p.GetExtensionData(); raw != nil {
		data, err := RawJSONFromProto(raw)
		if err != nil {
			return InvocationContentPart{}, err
		}
		part.ExtensionData = data
	}
	if p.ReasoningDialect != nil || p.ReasoningText != nil || p.GetReasoningOpaque() != nil || p.ReasoningSignature != nil ||
		p.GetReasoningSummary() != nil || p.GetReasoningContent() != nil || p.GetReasoningEncryptedContent() != nil {
		rp := &InvocationReasoningPart{}
		if p.ReasoningDialect != nil {
			v := p.GetReasoningDialect()
			rp.Dialect = &v
		}
		if p.ReasoningText != nil {
			v := p.GetReasoningText()
			rp.Text = &v
		}
		if p.ReasoningSignature != nil {
			v := p.GetReasoningSignature()
			rp.Signature = &v
		}
		if raw := p.GetReasoningOpaque(); raw != nil {
			opaque, err := RawJSONFromProto(raw)
			if err != nil {
				return InvocationContentPart{}, err
			}
			rp.Opaque = opaque
		}
		for _, r := range []struct {
			raw  *backendpluginv1.RawJSONValue
			dest *RawJSON
		}{{p.GetReasoningSummary(), &rp.Summary}, {p.GetReasoningContent(), &rp.Content}, {p.GetReasoningEncryptedContent(), &rp.EncryptedContent}} {
			if r.raw == nil {
				continue
			}
			val, err := RawJSONFromProto(r.raw)
			if err != nil {
				return InvocationContentPart{}, err
			}
			*r.dest = val
		}
		part.Reasoning = rp
	}
	return part, nil
}

func invocationContentPartToProto(part InvocationContentPart) (*backendpluginv1.InvocationContentPart, error) {
	kind, err := partKindToProto(part.Kind)
	if err != nil {
		return nil, err
	}
	out := &backendpluginv1.InvocationContentPart{Kind: kind}
	if part.Text != nil {
		out.Text = part.Text
	}
	if part.ImageRef != nil {
		out.ImageRef = part.ImageRef
	}
	if part.ImageMIME != nil {
		out.ImageMime = part.ImageMIME
	}
	if part.FileRef != nil {
		out.FileRef = part.FileRef
	}
	if part.FileMIME != nil {
		out.FileMime = part.FileMIME
	}
	if part.FileName != nil {
		out.FileName = part.FileName
	}
	if part.FileData != nil {
		out.FileData = part.FileData
	}
	if part.VideoRef != nil {
		out.VideoRef = part.VideoRef
	}
	if part.VideoMIME != nil {
		out.VideoMime = part.VideoMIME
	}
	if part.Refusal != nil {
		out.Refusal = part.Refusal
	}
	if part.Summary != nil {
		out.Summary = part.Summary
	}
	if part.AssistantRef != nil {
		out.AssistantRef = part.AssistantRef
	}
	if part.AnnotationType != nil {
		out.AnnotationType = part.AnnotationType
	}
	if part.AnnotationData.State() == RawJSONValue {
		out.AnnotationData = RawJSONToProto(part.AnnotationData)
	}
	if part.ExtensionType != nil {
		out.ExtensionType = part.ExtensionType
	}
	if part.ExtensionNamespace != nil {
		out.ExtensionNamespace = part.ExtensionNamespace
	}
	if part.ExtensionImplementor != nil {
		out.ExtensionImplementor = part.ExtensionImplementor
	}
	if part.ExtensionData.State() == RawJSONValue {
		out.ExtensionData = RawJSONToProto(part.ExtensionData)
	}
	if part.Reasoning != nil {
		if part.Reasoning.Dialect != nil {
			out.ReasoningDialect = part.Reasoning.Dialect
		}
		if part.Reasoning.Text != nil {
			out.ReasoningText = part.Reasoning.Text
		}
		if part.Reasoning.Signature != nil {
			out.ReasoningSignature = part.Reasoning.Signature
		}
		if part.Reasoning.Opaque.State() == RawJSONValue {
			out.ReasoningOpaque = RawJSONToProto(part.Reasoning.Opaque)
		}
		if part.Reasoning.Summary.State() == RawJSONValue {
			out.ReasoningSummary = RawJSONToProto(part.Reasoning.Summary)
		}
		if part.Reasoning.Content.State() == RawJSONValue {
			out.ReasoningContent = RawJSONToProto(part.Reasoning.Content)
		}
		if part.Reasoning.EncryptedContent.State() != RawJSONAbsent {
			out.ReasoningEncryptedContent = RawJSONToProto(part.Reasoning.EncryptedContent)
		}
	}
	return out, nil
}
