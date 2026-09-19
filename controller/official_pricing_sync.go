package controller

import (
	"context"
	"fmt"
	"io"
	"maps"
	"net/http"
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
)

var (
	officialPricingURL    = "https://basellm.github.io/llm-metadata/api/newapi/ratio_config-v1-base.json"
	officialPricingClient = &http.Client{Timeout: 20 * time.Second}
)

var officialPricingAliases = map[string]string{
	"deepseek-v4-flash-0731": "deepseek-v4-flash",
	"deepseek-v4.1-flash":    "deepseek-flash",
	"deepseek-v4-pro-0813":   "deepseek-v4-pro",
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
		configuredExpression, _ := entry.Configured[billing_setting.BillingExprField].(string)
		if billing_setting.IsManualBillingExpr(configuredExpression) {
			continue
		}
		expression, exists := expressions[entry.ModelName]
		if !exists {
			continue
		}
		draft := maps.Clone(entry.Configured)
		if draft == nil {
			draft = make(model.PricingValues)
		}
		draft[billing_setting.BillingModeField] = modes[entry.ModelName]
		draft[billing_setting.BillingExprField] = expression
		changes = append(changes, model.ModelPricingChange{
			ModelName: entry.ModelName, ExpectedVersion: entry.Version, Pricing: draft,
		})
	}
	if len(changes) == 0 {
		return nil
	}
	return model.UpdateModelPricing(changes)
}

func StartOfficialPricingRefreshTask() {
	go func() {
		if err := refreshOfficialPricing(context.Background()); err != nil {
			common.SysError(fmt.Sprintf("initial official pricing refresh failed: %v", err))
		}
		ticker := time.NewTicker(officialPricingRefreshInterval)
		defer ticker.Stop()
		for range ticker.C {
			if err := refreshOfficialPricing(context.Background()); err != nil {
				common.SysError(fmt.Sprintf("scheduled official pricing refresh failed: %v", err))
			}
		}
	}()
}
