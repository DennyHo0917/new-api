package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGPTModelContainingSparkUsesOpenAIVendor(t *testing.T) {
	meta := map[string]*Model{}
	vendors := map[int]*Vendor{}
	initDefaultVendorMapping(meta, vendors, []AbilityWithChannel{{Ability: Ability{Model: "gpt-5.3-codex-spark"}}})

	assert.Equal(t, defaultVendorDisplayIDs["OpenAI"], meta["gpt-5.3-codex-spark"].VendorID)
}
