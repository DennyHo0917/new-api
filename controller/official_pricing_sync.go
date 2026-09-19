package controller

import (
	"context"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/billing_setting"
)

const (
	officialPricingRefreshInterval = 24 * time.Hour
	officialPricingMaxBytes        = 10 << 20
	upstreamMediaMarkup            = 1.05
)

var (
	officialPricingURL         = "https://basellm.github.io/llm-metadata/api/newapi/ratio_config-v1-base.json"
	officialPricingClient      = &http.Client{Timeout: 20 * time.Second}
	upstreamMediaPricingURL    = "https://apiroute.subrouter.ai/api/dist/site/models"
	upstreamMediaPricingClient = &http.Client{Timeout: 20 * time.Second}
)

var officialPricingAliases = map[string]string{
	"deepseek-v4-flash-0731": "deepseek-v4-flash",
	"deepseek-v4.1-flash":    "deepseek-flash",
	"deepseek-v4-pro-0813":   "deepseek-v4-pro",
}

type upstreamMediaPrice struct {
	ModelName       string   `json:"model_name"`
	Category        string   `json:"category"`
	BillingType     string   `json:"billing_type"`
	BillingExpr     string   `json:"billing_expr"`
	FixedPrice      *float64 `json:"fixed_price"`
	PriceMultiplier *float64 `json:"price_multiplier"`
}

func (price upstreamMediaPrice) isImageOrVideo() bool {
	category := strings.ToLower(price.Category)
	return strings.Contains(category, "image") || strings.Contains(category, "video")
}

func (price upstreamMediaPrice) pricingValues() (model.PricingValues, bool) {
	if !price.isImageOrVideo() {
		return nil, false
	}
	switch price.BillingType {
	case "per_call":
		if price.FixedPrice == nil || *price.FixedPrice <= 0 {
			return nil, false
		}
		return model.PricingValues{"ModelPrice": *price.FixedPrice * upstreamMediaMarkup}, true
	case billing_setting.BillingModeTieredExpr:
		expression := strings.TrimSpace(price.BillingExpr)
		if expression == "" {
			return nil, false
		}
		multiplier := upstreamMediaMarkup
		if price.PriceMultiplier != nil && *price.PriceMultiplier > 0 {
			multiplier *= *price.PriceMultiplier
		}
		expression = scaleTierBillingExpression(expression, multiplier)
		if expression == "" {
			return nil, false
		}
		if err := billing_setting.SmokeTestExpr(expression); err != nil {
			return nil, false
		}
		return model.PricingValues{
			"billing_setting.billing_mode": billing_setting.BillingModeTieredExpr,
			"billing_setting.billing_expr": expression,
		}, true
	default:
		return nil, false
	}
}

func scaleTierBillingExpression(expression string, multiplier float64) string {
	start := strings.Index(expression, `tier("`)
	if start < 0 {
		return ""
	}
	depth := 0
	inString := false
	escaped := false
	comma := -1
	for index := start; index < len(expression); index++ {
		switch expression[index] {
		case '\\':
			if inString {
				escaped = !escaped
			}
			continue
		case '"':
			if !escaped {
				inString = !inString
			}
		}
		escaped = false
		if inString {
			continue
		}
		switch expression[index] {
		case '(':
			depth++
		case ',':
			if depth == 1 && comma < 0 {
				comma = index
			}
		case ')':
			depth--
			if depth == 0 {
				if comma < 0 {
					return ""
				}
				body := strings.TrimSpace(expression[comma+1 : index])
				return expression[:comma+1] + fmt.Sprintf(" (%s) * %.6f", body, multiplier) + expression[index:]
			}
		}
	}
	return ""
}

// peakBillingExpression replaces a generated peak/off-peak schedule with its
// peak tier. Other conditional pricing, such as context-length tiers, is kept.
func peakBillingExpression(expression string) string {
	if !strings.Contains(expression, `tier("off_peak",`) {
		return expression
	}
	start := strings.Index(expression, `tier("peak",`)
	if start < 0 {
		return expression
	}

	depth := 0
	inString := false
	escaped := false
	for index := start; index < len(expression); index++ {
		switch expression[index] {
		case '\\':
			if inString {
				escaped = !escaped
			}
			continue
		case '"':
			if !escaped {
				inString = !inString
			}
		}
		escaped = false
		if inString {
			continue
		}
		switch expression[index] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				peak := expression[start : index+1]
				if _, rules, found := strings.Cut(expression, "|||"); found {
					return peak + "|||" + rules
				}
				return peak
			}
		}
	}
	return expression
}

func loadOfficialPeakPricing(ctx context.Context, wanted map[string]bool) (map[string]string, map[string]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, officialPricingURL, nil)
	if err != nil {
		return nil, nil, err
	}
	resp, err := officialPricingClient.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("official pricing provider returned HTTP %d", resp.StatusCode)
	}

	var payload struct {
		Data map[string]any `json:"data"`
	}
	if err := common.DecodeJson(io.LimitReader(resp.Body, officialPricingMaxBytes), &payload); err != nil {
		return nil, nil, err
	}

	remoteModes := valueMap(payload.Data[billing_setting.BillingModeField])
	remoteExpressions := valueMap(payload.Data[billing_setting.BillingExprField])
	wantedRemote := maps.Clone(wanted)
	for alias, canonical := range officialPricingAliases {
		if wanted[alias] {
			wantedRemote[canonical] = true
		}
	}
	modes := make(map[string]string, len(wanted))
	expressions := make(map[string]string, len(wanted))
	for name, rawMode := range remoteModes {
		if !wantedRemote[name] {
			continue
		}
		mode, ok := rawMode.(string)
		if !ok || mode != billing_setting.BillingModeTieredExpr {
			continue
		}
		rawExpression, ok := remoteExpressions[name].(string)
		if !ok || strings.TrimSpace(rawExpression) == "" {
			continue
		}
		expression := peakBillingExpression(strings.TrimSpace(rawExpression))
		if err := billing_setting.SmokeTestExpr(expression); err != nil {
			logger.LogWarn(ctx, fmt.Sprintf("skip invalid official pricing for %s: %v", name, err))
			continue
		}
		modes[name] = mode
		expressions[name] = expression
	}
	for alias, canonical := range officialPricingAliases {
		if expression, ok := expressions[canonical]; ok && wanted[alias] {
			modes[alias] = billing_setting.BillingModeTieredExpr
			expressions[alias] = expression
		}
	}
	if len(expressions) == 0 {
		return nil, nil, fmt.Errorf("official pricing provider returned no valid prices")
	}
	return modes, expressions, nil
}

func refreshOfficialPricing(ctx context.Context) error {
	pricing := model.GetPricing()
	names := make([]string, 0, len(pricing))
	wanted := make(map[string]bool, len(pricing))
	for _, item := range pricing {
		names = append(names, item.ModelName)
		wanted[item.ModelName] = true
	}
	modes, expressions, err := loadOfficialPeakPricing(ctx, wanted)
	if err != nil {
		return err
	}
	snapshot, err := model.GetModelPricingSnapshot(names)
	if err != nil {
		return err
	}
	changes := make([]model.ModelPricingChange, 0, len(expressions))
	for _, entry := range snapshot.Entries {
		expression, exists := expressions[entry.ModelName]
		if !exists {
			continue
		}
		draft := maps.Clone(entry.Configured)
		if draft == nil {
			draft = make(model.PricingValues)
		}
		draft["billing_setting.billing_mode"] = modes[entry.ModelName]
		draft["billing_setting.billing_expr"] = expression
		changes = append(changes, model.ModelPricingChange{
			ModelName: entry.ModelName, ExpectedVersion: entry.Version, Pricing: draft,
		})
	}
	if len(changes) == 0 {
		return nil
	}
	return model.UpdateModelPricing(changes)
}

func loadUpstreamMediaPricing(ctx context.Context, wanted map[string]bool) (map[string]model.PricingValues, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, upstreamMediaPricingURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := upstreamMediaPricingClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("upstream media pricing returned HTTP %d", resp.StatusCode)
	}
	var payload struct {
		Success bool                 `json:"success"`
		Data    []upstreamMediaPrice `json:"data"`
		Message string               `json:"message"`
	}
	if err := common.DecodeJson(io.LimitReader(resp.Body, officialPricingMaxBytes), &payload); err != nil {
		return nil, err
	}
	if !payload.Success {
		return nil, fmt.Errorf("upstream media pricing failed: %s", payload.Message)
	}
	prices := make(map[string]model.PricingValues)
	for _, item := range payload.Data {
		if !wanted[item.ModelName] {
			continue
		}
		values, ok := item.pricingValues()
		if ok {
			prices[item.ModelName] = values
		}
	}
	if len(prices) == 0 {
		return nil, fmt.Errorf("upstream returned no usable image or video prices")
	}
	return prices, nil
}

func refreshUpstreamMediaPricing(ctx context.Context) error {
	pricing := model.GetPricing()
	names := make([]string, 0, len(pricing))
	wanted := make(map[string]bool, len(pricing))
	for _, item := range pricing {
		names = append(names, item.ModelName)
		wanted[item.ModelName] = true
	}
	prices, err := loadUpstreamMediaPricing(ctx, wanted)
	if err != nil {
		return err
	}
	snapshot, err := model.GetModelPricingSnapshot(names)
	if err != nil {
		return err
	}
	changes := make([]model.ModelPricingChange, 0, len(prices))
	for _, entry := range snapshot.Entries {
		upstream, exists := prices[entry.ModelName]
		if !exists {
			continue
		}
		draft := maps.Clone(entry.Configured)
		if draft == nil {
			draft = make(model.PricingValues)
		}
		for _, field := range []string{
			"ModelPrice", "ModelRatio", "CompletionRatio", "CacheRatio", "CreateCacheRatio",
			"ImageRatio", "AudioRatio", "AudioCompletionRatio",
			"billing_setting.billing_mode", "billing_setting.billing_expr",
		} {
			delete(draft, field)
		}
		for field, value := range upstream {
			draft[field] = value
		}
		changes = append(changes, model.ModelPricingChange{
			ModelName: entry.ModelName, ExpectedVersion: entry.Version, Pricing: draft,
		})
	}
	if len(changes) == 0 {
		return nil
	}
	if err := model.UpdateModelPricing(changes); err != nil {
		return err
	}
	channels, err := model.GetAllChannels(0, 0, true, true)
	if err != nil {
		return err
	}
	for _, channel := range channels {
		if channel.BaseURL == nil {
			continue
		}
		parsed, err := url.Parse(*channel.BaseURL)
		if err != nil || !strings.EqualFold(parsed.Hostname(), "apiroute.subrouter.ai") {
			continue
		}
		settings := channel.GetOtherSettings()
		for modelName := range prices {
			if !common.StringsContains(channel.GetModels(), modelName) || settings.ModelMultipliers[modelName] == 1 {
				continue
			}
			one := 1.0
			if err := model.SetChannelModelMultiplier(channel.Id, modelName, &one); err != nil {
				return err
			}
		}
	}
	return nil
}

func StartOfficialPricingRefreshTask() {
	go func() {
		if err := refreshOfficialPricing(context.Background()); err != nil {
			common.SysError(fmt.Sprintf("initial official pricing refresh failed: %v", err))
		}
		if err := refreshUpstreamMediaPricing(context.Background()); err != nil {
			common.SysError(fmt.Sprintf("initial upstream media pricing refresh failed: %v", err))
		}
		ticker := time.NewTicker(officialPricingRefreshInterval)
		defer ticker.Stop()
		for range ticker.C {
			if err := refreshOfficialPricing(context.Background()); err != nil {
				common.SysError(fmt.Sprintf("scheduled official pricing refresh failed: %v", err))
			}
			if err := refreshUpstreamMediaPricing(context.Background()); err != nil {
				common.SysError(fmt.Sprintf("scheduled upstream media pricing refresh failed: %v", err))
			}
		}
	}()
}
