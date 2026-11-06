# GIF and SVG output through the Images API

GPT Image 2.6 Flare and Sunburst support native animated GIF and static SVG
output. Use `/v1/images/generations`, or `/v1/images/edits` for GIF edits, rather
than constructing an animation from separate static images.

Source: [OpenAI image generation guide](https://developers.openai.com/api/docs/guides/image-generation),
checked November 6, 2026. Availability on an OAuth/Codex subscription account
still depends on that account's upstream endpoint and entitlements.

## Generate an animated GIF

```sh
curl "$SUB2API_BASE_URL/v1/images/generations" \
  -H "Authorization: Bearer $SUB2API_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "gpt-image-2.6-sunburst",
    "prompt": "A blue circle bouncing in a seamless loop on white.",
    "output_format": "gif",
    "frame_count": 9,
    "frame_duration_ms": 200,
    "size": "512x512"
  }'
```

GIF output accepts `frame_count` from 2 to 16 and `frame_duration_ms` from 20 to
2000. Omit them to use upstream defaults (9 frames and 200 milliseconds).
The gateway forwards the requested duration unchanged; the upstream rounds it
to 10 milliseconds. `size` is `auto` or positive `WIDTHxHEIGHT` dimensions that
are multiples of 16. The upstream also enforces model-specific total pixel
budgets across frames and animations.

For edits, send the same options with your input image to `/v1/images/edits`,
using JSON `images[].image_url` or multipart image uploads. Multipart
`frame_count` and `frame_duration_ms` are forwarded as integer fields on the
OAuth Images route.

## Generate an SVG

```sh
curl "$SUB2API_BASE_URL/v1/images/generations" \
  -H "Authorization: Bearer $SUB2API_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "gpt-image-2.6-flare",
    "prompt": "A simple editable vector logo: a blue cloud on white.",
    "output_format": "svg",
    "n": 1
  }'
```

SVG output is supported only on the generation endpoint with `n=1`. SVG input
and SVG output on the edit endpoint are not supported by the official API.

## Response and restrictions

Decode each response's `data[].b64_json` into file bytes and save with `.gif` or
`.svg`. Native animation frames, delays, loop metadata, and SVG XML are passed
through without re-encoding. On the OAuth route, `response_format=url` returns
an inline data URL with `image/gif` or `image/svg+xml` respectively. No file is
uploaded to a public hosting service.

Neither format supports streaming, `partial_images`, `output_compression`, or
`prompt_rewriter`. GIF frame options are valid only with `output_format=gif`.
Invalid combinations return HTTP 400 before account scheduling or generation.

GIF and SVG requests are never retried through the Responses image-generation
tool when the OAuth Images endpoint returns 404/405. The upstream error is
reported instead of silently degrading to a static raster image. Existing
PNG/JPEG/WebP fallback behavior is unchanged.

This change does not alter billing configuration or the choice between token
and per-image billing. It requires no new environment variables or database
migrations. Local regression tests use a generated two-frame GIF and SVG XML;
they do not establish live OAuth entitlement to these output formats.
