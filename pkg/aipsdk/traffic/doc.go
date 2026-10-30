// Package traffic defines four-leg observation, privileged raw capture, and redactor hooks
// (design §10–§11). [RawCaptureSink] is the stable name for privileged “capture sink” bytes;
// general traffic uses [Observer] on redacted or structured bodies only.
package traffic
