// Package toolcallrepair holds YAML config decode and bundle construction for the
// standard-distribution tool-call-repair feature (ADR 0007 / issue #152).
//
// The SDK Finalizer adapter and Engine live in internal/plugins/features/toolcallrepair/repair;
// FeatureBundle maps Config into that adapter and contributes PlaneToolCallFinalizers
// and PlaneToolCallFinalizationMaxArgsBytes.
// Enablement is config.PluginConfig.Enabled only (plain bool: omit means false /
// opt-out when a matching features row is present).
package toolcallrepair
