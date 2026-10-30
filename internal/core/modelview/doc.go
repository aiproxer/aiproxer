// Package modelview holds cycle-free aggregate request model-view identity:
// config generation/fingerprint, registry generation, catalog generation, and
// a stable digest for diagnostics and /v1/models ETag (req 9.6).
//
// It must not import modelregistry, modelcatalog, runtimehost, or stdhttp so
// composition roots can attach one identity alongside package-native BoundViews
// without creating package cycles.
package modelview
