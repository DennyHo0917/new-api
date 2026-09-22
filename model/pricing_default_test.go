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

func TestExistingByteDanceModelGetsDefaultVendorAndLogo(t *testing.T) {
	const modelName = "bytedance/seedance-2-5"
	meta := map[string]*Model{modelName: {ModelName: modelName, Status: 1}}
	vendors := map[int]*Vendor{}
	initDefaultVendorMapping(meta, vendors, []AbilityWithChannel{{Ability: Ability{Model: modelName}}})

	vendorID := defaultVendorDisplayIDs["字节跳动"]
	assert.Equal(t, vendorID, meta[modelName].VendorID)
	assert.Equal(t, "字节跳动", vendors[vendorID].Name)
	assert.Equal(t, "Doubao.Color", vendors[vendorID].Icon)
}
