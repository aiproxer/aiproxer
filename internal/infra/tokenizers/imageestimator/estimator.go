// Package imageestimator provides bounded local estimates for multimodal image parts.
package imageestimator

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	_ "image/gif"  // register GIF decoder for image.DecodeConfig
	_ "image/jpeg" // register JPEG decoder for image.DecodeConfig
	_ "image/png"  // register PNG decoder for image.DecodeConfig
	"math"
	"strings"
)

const (
	DefaultBaseTokens      = 85
	DefaultMaxDecodedBytes = 5 * 1024 * 1024
	maxBase64Overhead      = 8
)

var ErrUnavailable = errors.New("image token estimate unavailable")

// Config controls bounded, local-only image token estimation. It never fetches URLs.
type Config struct {
	BaseTokens       int
	MaxDecodedBytes  int
	UseDefaultTokens bool
	DefaultTokens    int
}

// Estimator applies OpenAI-style image token rules where dimensions are known.
type Estimator struct {
	baseTokens      int
	maxDecodedBytes int
	useDefault      bool
	defaultTokens   int
}

type Input struct {
	Ref    string
	Detail string
}

func New(cfg Config) Estimator {
	baseTokens := cfg.BaseTokens
	if baseTokens <= 0 {
		baseTokens = DefaultBaseTokens
	}
	maxDecodedBytes := cfg.MaxDecodedBytes
	if maxDecodedBytes <= 0 {
		maxDecodedBytes = DefaultMaxDecodedBytes
	}
	defaultTokens := cfg.DefaultTokens
	if defaultTokens <= 0 {
		defaultTokens = baseTokens
	}
	return Estimator{
		baseTokens:      baseTokens,
		maxDecodedBytes: maxDecodedBytes,
		useDefault:      cfg.UseDefaultTokens,
		defaultTokens:   defaultTokens,
	}
}

// Count estimates OpenAI-style image tokens. Low and auto use the base estimate; high uses 512px tiles after
// scaling the image to a 768px short side with a 2000px long-side cap.
func (e Estimator) Count(input Input) (int, error) {
	width, height, ok, err := e.dimensions(input.Ref)
	if err != nil {
		return 0, err
	}
	if !ok {
		if e.useDefault {
			return e.defaultTokens, nil
		}
		return 0, fmt.Errorf("%w: image dimensions unavailable", ErrUnavailable)
	}

	switch strings.ToLower(strings.TrimSpace(input.Detail)) {
	case "high":
		return e.highDetailTokens(width, height), nil
	case "", "auto", "low":
		return e.baseTokens, nil
	default:
		return 0, fmt.Errorf("%w: unsupported image detail", ErrUnavailable)
	}
}

func (e Estimator) highDetailTokens(width, height int) int {
	longSide := float64(max(width, height))
	shortSide := float64(min(width, height))
	if shortSide != 768 {
		scale := 768 / shortSide
		longSide *= scale
		shortSide *= scale
	}
	if longSide > 2000 {
		scale := 2000 / longSide
		longSide *= scale
		shortSide *= scale
	}
	tilesWide := int(math.Ceil(longSide / 512))
	tilesHigh := int(math.Ceil(shortSide / 512))
	return e.baseTokens + tilesWide*tilesHigh*(e.baseTokens*2)
}

func (e Estimator) dimensions(ref string) (int, int, bool, error) {
	data, ok, err := e.dataURIBytes(ref)
	if err != nil || !ok {
		return 0, 0, ok, err
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return 0, 0, true, fmt.Errorf("%w: image data cannot be decoded", ErrUnavailable)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return 0, 0, true, fmt.Errorf("%w: image dimensions invalid", ErrUnavailable)
	}
	return cfg.Width, cfg.Height, true, nil
}

func (e Estimator) dataURIBytes(ref string) ([]byte, bool, error) {
	if !strings.HasPrefix(ref, "data:") {
		return nil, false, nil
	}
	meta, payload, ok := strings.Cut(ref, ",")
	if !ok || !strings.Contains(strings.ToLower(meta), ";base64") || !strings.HasPrefix(strings.ToLower(meta), "data:image/") {
		return nil, true, fmt.Errorf("%w: unsupported image data uri", ErrUnavailable)
	}
	if len(payload) > encodedLimit(e.maxDecodedBytes) {
		return nil, true, fmt.Errorf("%w: image data exceeds local decode limit", ErrUnavailable)
	}
	decoded, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return nil, true, fmt.Errorf("%w: invalid image base64", ErrUnavailable)
	}
	if len(decoded) > e.maxDecodedBytes {
		return nil, true, fmt.Errorf("%w: image data exceeds local decode limit", ErrUnavailable)
	}
	return decoded, true, nil
}

func encodedLimit(decodedLimit int) int {
	return ((decodedLimit + 2) / 3 * 4) + maxBase64Overhead
}
