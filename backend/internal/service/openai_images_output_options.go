package service

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
)

// GIF/SVG are Image API features. Do not fall back to the Responses tool,
// which cannot preserve the requested animation/vector output contract.
func requiresOpenAIImagesAPI(req *OpenAIImagesRequest) bool {
	if req == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(req.OutputFormat)) {
	case "gif", "svg":
		return true
	default:
		return false
	}
}

func parseOpenAIImagesFrameInteger(value gjson.Result, name string) (int, error) {
	// Check before conversion: gjson.Int silently truncates fractional numbers.
	n := value.Float()
	if value.Type != gjson.Number || math.IsNaN(n) || math.IsInf(n, 0) || math.Trunc(n) != n || n < 0 || n > 2000 {
		return 0, fmt.Errorf("%s must be an integer within its supported range", name)
	}
	return int(n), nil
}

// https://developers.openai.com/api/docs/guides/image-generation
// Preserve omitted fields so the upstream remains responsible for defaults,
// frame-duration rounding, and the model's total animation pixel budget.
func validateOpenAIImagesOutputOptions(req *OpenAIImagesRequest) error {
	format := strings.ToLower(strings.TrimSpace(req.OutputFormat))
	if req.FrameCount != nil || req.FrameDurationMS != nil {
		if format != "gif" {
			return fmt.Errorf("frame_count and frame_duration_ms require output_format=gif")
		}
	}
	if !requiresOpenAIImagesAPI(req) {
		return nil
	}
	if req.Stream {
		return fmt.Errorf("output_format=%s does not support streaming", format)
	}
	if req.PartialImages != nil || req.OutputCompression != nil || req.HasPromptRewriter {
		return fmt.Errorf("output_format=%s does not support partial_images, output_compression, or prompt_rewriter", format)
	}
	if format == "svg" {
		if req.IsEdits() {
			return fmt.Errorf("output_format=svg is only supported on /v1/images/generations")
		}
		if req.N != 1 {
			return fmt.Errorf("output_format=svg requires n=1")
		}
		return nil
	}
	if req.FrameCount != nil && (*req.FrameCount < 2 || *req.FrameCount > 16) {
		return fmt.Errorf("frame_count must be between 2 and 16")
	}
	if req.FrameDurationMS != nil && (*req.FrameDurationMS < 20 || *req.FrameDurationMS > 2000) {
		return fmt.Errorf("frame_duration_ms must be between 20 and 2000")
	}
	if size := strings.TrimSpace(req.Size); size != "" && size != "auto" {
		width, height, ok := strings.Cut(size, "x")
		w, werr := strconv.Atoi(width)
		h, herr := strconv.Atoi(height)
		if !ok || werr != nil || herr != nil || w <= 0 || h <= 0 || w%16 != 0 || h%16 != 0 {
			return fmt.Errorf("GIF size must be auto or WIDTHxHEIGHT with positive dimensions divisible by 16")
		}
	}
	return nil
}
