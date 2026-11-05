package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestGPTImage26PricingAndBilling(t *testing.T) {
	catalog, err := os.ReadFile("../../resources/model-pricing/model_prices_and_context_window.json")
	require.NoError(t, err)
	parser := &PricingService{}
	prices, err := parser.parsePricingData(catalog)
	require.NoError(t, err)

	for _, model := range []string{"gpt-image-2.6-flare", "gpt-image-2.6-sunburst"} {
		// Non-cached input: text 10 + image 40. Cached: text 10 + image 40.
		tokens := UsageTokens{InputTokens: 50, ImageInputTokens: 40, CacheReadTokens: 50, ImageCacheReadTokens: 40, OutputTokens: 200, ImageOutputTokens: 200}
		for _, test := range []struct {
			name string
			svc  *PricingService
		}{
			{"no_service", nil},
			{"empty_catalog", &PricingService{}},
			{"legacy_catalog", &PricingService{pricingData: map[string]*LiteLLMModelPricing{
				"gpt-image-1.5": {InputCostPerToken: 99e-6, OutputCostPerImageToken: 32e-6},
				"gpt-image-2":   {InputCostPerToken: 99e-6, OutputCostPerImageToken: 99e-6},
			}}},
			{"bundled_catalog", &PricingService{pricingData: prices}},
			{"image_only_catalog", &PricingService{pricingData: map[string]*LiteLLMModelPricing{
				model: {OutputCostPerImage: 0.5, TokenPricingAbsent: true},
			}}},
		} {
			t.Run(model+"/"+test.name, func(t *testing.T) {
				if test.svc != nil && test.name != "image_only_catalog" {
					p := test.svc.GetModelPricing(model)
					require.NotNil(t, p)
					require.Equal(t, 5e-6, p.InputCostPerToken)
					require.Equal(t, 1.25e-6, p.CacheReadInputTokenCost)
					require.Equal(t, 8e-6, p.InputCostPerImageToken)
					require.Equal(t, 2e-6, p.CacheReadInputImageTokenCost)
					require.Equal(t, 30e-6, p.OutputCostPerImageToken)
					require.Zero(t, p.OutputCostPerToken)
				}
				billing := NewBillingService(&config.Config{}, test.svc)
				require.True(t, billing.HasIdentifiedTokenPricing(model))
				p, err := billing.GetModelPricing(model)
				require.NoError(t, err)
				require.Equal(t, 5e-6, p.InputPricePerToken)
				require.Equal(t, 1.25e-6, p.CacheReadPricePerToken)
				require.Equal(t, 8e-6, p.ImageInputPricePerToken)
				require.Equal(t, 2e-6, p.ImageCacheReadPricePerToken)
				require.Equal(t, 30e-6, p.ImageOutputPricePerToken)
				require.Zero(t, p.OutputPricePerToken)
				cost, err := billing.CalculateCost(model, tokens, 1)
				require.NoError(t, err)
				require.InDelta(t, 0.00005, cost.InputCost, 1e-12)
				require.InDelta(t, 0.00032, cost.ImageInputCost, 1e-12)
				require.InDelta(t, 0.0000925, cost.CacheReadCost, 1e-12)
				require.InDelta(t, 0.006, cost.ImageOutputCost, 1e-12)
				require.InDelta(t, 0.0064625, cost.TotalCost, 1e-12)
			})
		}
		t.Run(model+"/explicit_override", func(t *testing.T) {
			custom := &LiteLLMModelPricing{InputCostPerToken: 7e-6, CacheReadInputTokenCost: 3e-6, InputCostPerImageToken: 9e-6, CacheReadInputImageTokenCost: 4e-6, OutputCostPerImageToken: 40e-6}
			svc := &PricingService{pricingData: map[string]*LiteLLMModelPricing{model: custom}}
			require.Same(t, custom, svc.GetModelPricing(model))
			cost, err := NewBillingService(&config.Config{}, svc).CalculateCost(model, tokens, 1)
			require.NoError(t, err)
			require.InDelta(t, 10*7e-6+40*9e-6+10*3e-6+40*4e-6+200*40e-6, cost.TotalCost, 1e-12)
		})
	}
}

func TestGPTImage26AccountTestMapsOnce(t *testing.T) {
	account := directImagesTestAccount()
	account.Credentials["model_mapping"] = map[string]any{
		"gpt-image-2":         "gpt-image-2.6-flare",
		"gpt-image-2.6-flare": "gpt-image-2.6-sunburst",
	}
	c, rec := newOpenAIImagesTestContext(t, []byte(`{}`))
	upstream := &httpUpstreamRecorder{resp: openAIImagesJSONResponse()}
	svc := &AccountTestService{httpUpstream: upstream}
	require.NoError(t, svc.testOpenAIAccountConnection(c, account, "gpt-image-2", "draw a cloud", ""))
	require.Equal(t, "/backend-api/codex/images/generations", upstream.lastReq.URL.Path)
	require.Equal(t, "gpt-image-2.6-flare", gjson.GetBytes(upstream.lastBody, "model").String())
	require.Contains(t, rec.Body.String(), `"success":true`)
}

func TestGPTImage26Streaming(t *testing.T) {
	for _, model := range []string{"gpt-image-2.6-flare", "gpt-image-2.6-sunburst"} {
		t.Run(model, func(t *testing.T) {
			body := []byte(fmt.Sprintf(`{"model":%q,"prompt":"draw a cloud","stream":true,"complexity":"low"}`, model))
			c, rec := newOpenAIImagesTestContext(t, body)
			stream := fmt.Sprintf("data: {\"type\":\"image_generation.completed\",\"model\":%q,\"b64_json\":\"aGVsbG8=\",\"usage\":{\"input_tokens\":10,\"output_tokens\":20}}\n\n", model)
			upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(stream))}}
			svc := newOpenAIImagesTestService(upstream)
			parsed, err := svc.ParseOpenAIImagesRequest(c, body)
			require.NoError(t, err)
			result, err := svc.ForwardImages(context.Background(), c, directImagesTestAccount(), body, parsed, "")
			require.NoError(t, err)
			require.Equal(t, "/backend-api/codex/images/generations", upstream.lastReq.URL.Path)
			require.Equal(t, "low", gjson.GetBytes(upstream.lastBody, "complexity").String())
			require.Equal(t, 1, result.ImageCount)
			require.Equal(t, 20, result.Usage.ImageOutputTokens)
			require.Equal(t, model, result.UpstreamResponseModel)
			require.Contains(t, rec.Body.String(), "event: image_generation.completed")
		})
	}
}
