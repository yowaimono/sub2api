package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func gifSVGRequest(t *testing.T, fields map[string]any, edits, form bool) ([]byte, string) {
	t.Helper()
	payload := map[string]any{"model": "gpt-image-2.6-sunburst", "prompt": "A blue circle bouncing in a seamless loop"}
	for k, v := range fields {
		payload[k] = v
	}
	if !form {
		if edits {
			payload["images"] = []map[string]string{{"image_url": "data:image/png;base64,AA=="}}
		}
		body, err := json.Marshal(payload)
		require.NoError(t, err)
		return body, "application/json"
	}
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for k, v := range payload {
		require.NoError(t, w.WriteField(k, fmt.Sprint(v)))
	}
	if edits {
		part, err := w.CreateFormFile("image", "source.png")
		require.NoError(t, err)
		_, err = part.Write([]byte("source-image"))
		require.NoError(t, err)
	}
	require.NoError(t, w.Close())
	return buf.Bytes(), w.FormDataContentType()
}

func TestOpenAIImagesGIFSVGValidation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		fields map[string]any
		edits  bool
		err    string
	}{
		{"gif_defaults", map[string]any{"output_format": "gif"}, false, ""},
		{"gif_edit_defaults", map[string]any{"output_format": "gif"}, true, ""},
		{"gif_min", map[string]any{"output_format": "gif", "frame_count": 2, "frame_duration_ms": 20, "size": "16x32"}, false, ""},
		{"gif_max", map[string]any{"output_format": "gif", "frame_count": 16, "frame_duration_ms": 2000, "size": "auto"}, true, ""},
		{"gif_upstream_rounding", map[string]any{"output_format": "gif", "frame_duration_ms": 205}, false, ""},
		{"gif_multiple_animations", map[string]any{"output_format": "gif", "n": 2}, false, ""},
		{"svg_defaults", map[string]any{"output_format": "svg"}, false, ""},
		{"svg_n1", map[string]any{"output_format": "svg", "n": 1}, false, ""},
		{"svg_edit", map[string]any{"output_format": "svg"}, true, "only supported"},
		{"svg_multiple", map[string]any{"output_format": "svg", "n": 2}, false, "n=1"},
		{"svg_fractional_n", map[string]any{"output_format": "svg", "n": 1.5}, false, "integer"},
		{"gif_stream", map[string]any{"output_format": "gif", "stream": true}, false, "streaming"},
		{"svg_stream", map[string]any{"output_format": "svg", "stream": true}, false, "streaming"},
		{"gif_partial_zero", map[string]any{"output_format": "gif", "partial_images": 0}, false, "partial_images"},
		{"svg_partial", map[string]any{"output_format": "svg", "partial_images": 1}, false, "partial_images"},
		{"gif_compression", map[string]any{"output_format": "gif", "output_compression": 100}, false, "output_compression"},
		{"svg_compression", map[string]any{"output_format": "svg", "output_compression": 0}, false, "output_compression"},
		{"gif_rewriter", map[string]any{"output_format": "gif", "prompt_rewriter": false}, false, "prompt_rewriter"},
		{"svg_rewriter", map[string]any{"output_format": "svg", "prompt_rewriter": true}, false, "prompt_rewriter"},
		{"frames_without_format", map[string]any{"frame_count": 9}, false, "require output_format=gif"},
		{"duration_png", map[string]any{"output_format": "png", "frame_duration_ms": 200}, false, "require output_format=gif"},
		{"frames_svg", map[string]any{"output_format": "svg", "frame_count": 9}, false, "require output_format=gif"},
		{"frames_low", map[string]any{"output_format": "gif", "frame_count": 1}, false, "frame_count"},
		{"frames_high", map[string]any{"output_format": "gif", "frame_count": 17}, false, "frame_count"},
		{"frames_negative", map[string]any{"output_format": "gif", "frame_count": -2}, false, "frame_count"},
		{"frames_fraction", map[string]any{"output_format": "gif", "frame_count": 2.5}, false, "integer"},
		{"frames_string", map[string]any{"output_format": "gif", "frame_count": "nine"}, false, "integer"},
		{"frames_bool", map[string]any{"output_format": "gif", "frame_count": true}, false, "integer"},
		{"frames_null", map[string]any{"output_format": "gif", "frame_count": nil}, false, "integer"},
		{"frames_huge", map[string]any{"output_format": "gif", "frame_count": 1e30}, false, "frame_count"},
		{"duration_low", map[string]any{"output_format": "gif", "frame_duration_ms": 19}, false, "frame_duration_ms"},
		{"duration_high", map[string]any{"output_format": "gif", "frame_duration_ms": 2001}, false, "frame_duration_ms"},
		{"duration_fraction", map[string]any{"output_format": "gif", "frame_duration_ms": 100.5}, false, "integer"},
		{"duration_null", map[string]any{"output_format": "gif", "frame_duration_ms": nil}, false, "integer"},
		{"gif_size_zero", map[string]any{"output_format": "gif", "size": "0x512"}, false, "GIF size"},
		{"gif_size_fraction", map[string]any{"output_format": "gif", "size": "512.5x512"}, false, "GIF size"},
		{"gif_size_alignment", map[string]any{"output_format": "gif", "size": "513x512"}, false, "GIF size"},
		{"gif_size_malformed", map[string]any{"output_format": "gif", "size": "512x512x2"}, false, "GIF size"},
		{"png_stream_unchanged", map[string]any{"output_format": "png", "stream": true, "partial_images": 1}, false, ""},
	} {
		for _, form := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/multipart=%t", tc.name, form), func(t *testing.T) {
				body, contentType := gifSVGRequest(t, tc.fields, tc.edits, form)
				c, _ := newOpenAIImagesTestContext(t, body)
				c.Request.Header.Set("Content-Type", contentType)
				if tc.edits {
					c.Request.URL.Path = openAIImagesEditsEndpoint
				}
				parsed, err := (&OpenAIGatewayService{}).ParseOpenAIImagesRequest(c, body)
				if tc.err != "" {
					require.ErrorContains(t, err, tc.err)
					require.Nil(t, parsed)
					return
				}
				require.NoError(t, err)
				require.True(t, parsed.HasNativeOptions)
				require.Equal(t, OpenAIImagesCapabilityNative, parsed.RequiredCapability)
			})
		}
	}
}

func TestOpenAIImagesGIFFrameForwarding(t *testing.T) {
	for _, model := range []string{"gpt-image-2.6-flare", "gpt-image-2.6-sunburst"} {
		for _, edits := range []bool{false, true} {
			for _, form := range []bool{false, true} {
				for _, explicit := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/edit=%t/form=%t/explicit=%t", model, edits, form, explicit), func(t *testing.T) {
						fields := map[string]any{"model": model, "output_format": "gif", "size": "512x512"}
						if explicit {
							fields["frame_count"], fields["frame_duration_ms"] = 9, 205
						}
						body, contentType := gifSVGRequest(t, fields, edits, form)
						c, _ := newOpenAIImagesTestContext(t, body)
						c.Request.Header.Set("Content-Type", contentType)
						endpoint := "/backend-api/codex/images/generations"
						if edits {
							c.Request.URL.Path = openAIImagesEditsEndpoint
							endpoint = "/backend-api/codex/images/edits"
						}
						upstream := &httpUpstreamRecorder{resp: openAIImagesJSONResponse()}
						svc := newOpenAIImagesTestService(upstream)
						parsed, err := svc.ParseOpenAIImagesRequest(c, body)
						require.NoError(t, err)
						_, err = svc.ForwardImages(context.Background(), c, directImagesTestAccount(), body, parsed, "")
						require.NoError(t, err)
						require.Equal(t, endpoint, upstream.lastReq.URL.Path)
						require.Equal(t, "gif", gjson.GetBytes(upstream.lastBody, "output_format").String())
						for key, want := range map[string]int{"frame_count": 9, "frame_duration_ms": 205} {
							got := gjson.GetBytes(upstream.lastBody, key)
							require.Equal(t, explicit, got.Exists(), key)
							if explicit {
								require.Equal(t, gjson.Number, got.Type)
								require.EqualValues(t, want, got.Int())
							}
						}
						if edits {
							require.Len(t, gjson.GetBytes(upstream.lastBody, "images").Array(), 1)
						}
					})
				}
			}
		}
	}
}

func TestOpenAIImagesGIFSVGResponsePreservesFile(t *testing.T) {
	var animation bytes.Buffer
	palette := color.Palette{color.White, color.Black}
	frame := image.NewPaletted(image.Rect(0, 0, 16, 32), palette)
	require.NoError(t, gif.EncodeAll(&animation, &gif.GIF{Image: []*image.Paletted{frame, frame}, Delay: []int{20, 30}, LoopCount: 0}))
	for _, tc := range []struct {
		format, mime string
		file         []byte
	}{
		{"gif", "image/gif", animation.Bytes()},
		{"svg", "image/svg+xml", []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 32 32"><circle cx="16" cy="16" r="8" fill="blue"/></svg>`)},
	} {
		for _, responseFormat := range []string{"b64_json", "url"} {
			for _, metadata := range []string{"item", "root", "omitted"} {
				t.Run(tc.format+"/"+responseFormat+"/"+metadata, func(t *testing.T) {
					body, contentType := gifSVGRequest(t, map[string]any{"output_format": tc.format, "response_format": responseFormat}, false, false)
					c, rec := newOpenAIImagesTestContext(t, body)
					c.Request.Header.Set("Content-Type", contentType)
					encoded := base64.StdEncoding.EncodeToString(tc.file)
					item := map[string]any{"b64_json": encoded, "frame_count": 2, "frame_duration_ms": 200}
					response := map[string]any{"data": []any{item}, "usage": map[string]any{"input_tokens": 20, "output_tokens": 515}}
					if metadata == "item" {
						item["output_format"] = tc.format
					} else if metadata == "root" {
						response["output_format"] = tc.format
					}
					upstreamBody, err := json.Marshal(response)
					require.NoError(t, err)
					upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(bytes.NewReader(upstreamBody))}}
					svc := newOpenAIImagesTestService(upstream)
					parsed, err := svc.ParseOpenAIImagesRequest(c, body)
					require.NoError(t, err)
					result, err := svc.ForwardImages(context.Background(), c, directImagesTestAccount(), body, parsed, "")
					require.NoError(t, err)
					require.Equal(t, http.StatusOK, rec.Code)
					require.Equal(t, 1, result.ImageCount, "animation frames are not separate generated images")
					require.Equal(t, 515, result.Usage.ImageOutputTokens)
					got := gjson.GetBytes(rec.Body.Bytes(), "data.0.b64_json").String()
					if responseFormat == "url" {
						require.False(t, gjson.GetBytes(rec.Body.Bytes(), "data.0.b64_json").Exists())
						prefix := "data:" + tc.mime + ";base64,"
						url := gjson.GetBytes(rec.Body.Bytes(), "data.0.url").String()
						require.True(t, strings.HasPrefix(url, prefix), url)
						got = strings.TrimPrefix(url, prefix)
					}
					file, err := base64.StdEncoding.DecodeString(got)
					require.NoError(t, err)
					require.Equal(t, tc.file, file)
					if tc.format == "gif" {
						decoded, err := gif.DecodeAll(bytes.NewReader(file))
						require.NoError(t, err)
						require.Len(t, decoded.Image, 2)
						require.Equal(t, []int{20, 30}, decoded.Delay)
						require.Equal(t, 0, decoded.LoopCount)
						require.Equal(t, "16x32", gjson.GetBytes(rec.Body.Bytes(), "data.0.size").String())
					}
				})
			}
		}
	}
}

func TestOpenAIImagesGIFSVGNoResponsesFallback(t *testing.T) {
	for _, format := range []string{"gif", "svg"} {
		for _, status := range []int{http.StatusNotFound, http.StatusMethodNotAllowed} {
			t.Run(fmt.Sprintf("%s/%d", format, status), func(t *testing.T) {
				body, _ := gifSVGRequest(t, map[string]any{"output_format": format}, false, false)
				c, _ := newOpenAIImagesTestContext(t, body)
				upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"endpoint unavailable"}}`))}}
				svc := newOpenAIImagesTestService(upstream)
				parsed, err := svc.ParseOpenAIImagesRequest(c, body)
				require.NoError(t, err)
				result, err := svc.ForwardImages(context.Background(), c, directImagesTestAccount(), body, parsed, "")
				require.Error(t, err)
				require.Nil(t, result)
				require.Len(t, upstream.bodies, 1)
				require.Equal(t, "/backend-api/codex/images/generations", upstream.lastReq.URL.Path)
				_, err = buildOpenAIImagesResponsesRequest(parsed, "gpt-image-2.6-sunburst")
				require.ErrorContains(t, err, "requires the Images API")
			})
		}
	}
}

func TestOpenAIImagesGIFSVGAPIKeyPassthrough(t *testing.T) {
	for _, format := range []string{"gif", "svg"} {
		for _, form := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/form=%t", format, form), func(t *testing.T) {
				fields := map[string]any{"output_format": format}
				if format == "gif" {
					fields["frame_count"], fields["frame_duration_ms"] = 9, 205
				}
				body, contentType := gifSVGRequest(t, fields, format == "gif", form)
				c, _ := newOpenAIImagesTestContext(t, body)
				c.Request.Header.Set("Content-Type", contentType)
				if format == "gif" {
					c.Request.URL.Path = openAIImagesEditsEndpoint
				}
				upstream := &httpUpstreamRecorder{resp: openAIImagesJSONResponse()}
				svc := newOpenAIImagesTestService(upstream)
				parsed, err := svc.ParseOpenAIImagesRequest(c, body)
				require.NoError(t, err)
				_, err = svc.ForwardImages(context.Background(), c, newOpenAIImagesAPIKeyAccount(), body, parsed, "")
				require.NoError(t, err)
				if !form {
					require.Equal(t, body, upstream.lastBody)
					return
				}
				// Model rewriting rebuilds the multipart boundary; field values and
				// uploaded files must survive unchanged.
				roundTrip := &OpenAIImagesRequest{Endpoint: parsed.Endpoint}
				require.NoError(t, parseOpenAIImagesMultipartRequest(upstream.lastBody, upstream.lastReq.Header.Get("Content-Type"), roundTrip))
				require.Equal(t, parsed.OutputFormat, roundTrip.OutputFormat)
				require.Equal(t, parsed.FrameCount, roundTrip.FrameCount)
				require.Equal(t, parsed.FrameDurationMS, roundTrip.FrameDurationMS)
				require.Equal(t, parsed.Prompt, roundTrip.Prompt)
				require.Equal(t, parsed.Uploads, roundTrip.Uploads)
			})
		}
	}
}
