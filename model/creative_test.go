package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
)

func setupCreativeModelTestDB(t *testing.T) {
	t.Helper()

	common.UsingSQLite = true
	common.UsingMySQL = false
	common.UsingPostgreSQL = false
	common.RedisEnabled = false
	require.NoError(t, DB.AutoMigrate(&CreativeModelPreference{}, &CreativeDocument{}, &CreativeAsset{}, &CreativeDocumentAssetRef{}))
	require.NoError(t, DB.Exec("DELETE FROM creative_model_preferences").Error)
	require.NoError(t, DB.Exec("DELETE FROM creative_documents").Error)
	require.NoError(t, DB.Exec("DELETE FROM creative_assets").Error)
	require.NoError(t, DB.Exec("DELETE FROM creative_document_asset_refs").Error)
	t.Cleanup(func() {
		DB.Exec("DELETE FROM creative_document_asset_refs")
		DB.Exec("DELETE FROM creative_assets")
		DB.Exec("DELETE FROM creative_model_preferences")
		DB.Exec("DELETE FROM creative_documents")
	})
}

func TestPatchCreativeModelPreferenceRejectsStaleBaseRevision(t *testing.T) {
	setupCreativeModelTestDB(t)

	created, conflict, err := PatchCreativeModelPreference(7, 0, CreativeModelPreferenceValue{
		Default: map[string]CreativeModelSafeSelection{
			"text": {ModelId: "gpt-4o", ProfileId: "primary-text"},
		},
		Pinned: []CreativeModelSafeSelection{{ModelId: "gpt-4o"}},
	})
	require.NoError(t, err)
	require.False(t, conflict)
	require.Equal(t, 1, created.Revision)

	updated, conflict, err := PatchCreativeModelPreference(7, 0, CreativeModelPreferenceValue{
		Default: map[string]CreativeModelSafeSelection{
			"text": {ModelId: "claude-3-5-sonnet"},
		},
	})
	require.NoError(t, err)
	require.True(t, conflict)
	require.Equal(t, 1, updated.Revision)

	stored, exists, err := GetCreativeModelPreference(7)
	require.NoError(t, err)
	require.True(t, exists)
	require.Equal(t, 1, stored.Revision)
	require.Contains(t, stored.PreferenceJSON, "gpt-4o")
	require.NotContains(t, stored.PreferenceJSON, "claude-3-5-sonnet")
}

func TestSanitizeCreativeModelPreferenceDropsPolicyAndSecretFields(t *testing.T) {
	safe, err := SanitizeCreativeModelPreference(map[string]any{
		"default": map[string]any{
			"text": map[string]any{
				"modelId":        "gpt-4o",
				"profileId":      "profile-text",
				"providerIdHint": "provider-hint",
				"vendorHint":     "openai",
				"updatedAt":      float64(1700000000000),
				"apiKey":         "must-not-persist",
				"baseURL":        "must-not-persist",
				"extra":          "must-not-persist",
			},
			"image": map[string]any{
				"modelId":   "image-model",
				"updatedAt": int64(1700000000001),
			},
			"invalid": map[string]any{"profileId": "missing-model-id"},
		},
		"pinned": []any{
			map[string]any{"modelId": "gpt-4o", "updatedAt": float64(1700000000002), "provider": "must-not-persist"},
			map[string]any{"profileId": "missing-model-id"},
			42,
		},
		"recent":               []any{map[string]any{"modelId": "claude", "vendorHint": "anthropic", "extra": "must-not-persist"}},
		"displayMode":          "compact",
		"customOrder":          []any{"claude", "gpt-4o"},
		"defaultModel":         "must-not-persist",
		"order":                []any{"must-not-persist"},
		"group":                "must-not-persist",
		"defaultVisibleModels": []any{"must-not-persist"},
		"apiKey":               "must-not-persist",
	})
	require.NoError(t, err)
	require.Equal(t, "gpt-4o", safe.Default["text"].ModelId)
	require.Equal(t, "profile-text", safe.Default["text"].ProfileId)
	require.Equal(t, "provider-hint", safe.Default["text"].ProviderIdHint)
	require.Equal(t, "openai", safe.Default["text"].VendorHint)
	require.NotNil(t, safe.Default["text"].UpdatedAt)
	require.Equal(t, int64(1700000000000), *safe.Default["text"].UpdatedAt)
	require.Equal(t, "image-model", safe.Default["image"].ModelId)
	require.NotContains(t, safe.Default, "invalid")
	require.Equal(t, []CreativeModelSafeSelection{{ModelId: "gpt-4o", UpdatedAt: int64Ptr(1700000000002)}}, safe.Pinned)
	require.Equal(t, []CreativeModelSafeSelection{{ModelId: "claude", VendorHint: "anthropic"}}, safe.Recent)
	require.Equal(t, "compact", safe.DisplayMode)
	require.Equal(t, []string{"claude", "gpt-4o"}, safe.CustomOrder)

	encoded, err := common.Marshal(safe)
	require.NoError(t, err)
	jsonText := string(encoded)
	require.Contains(t, jsonText, "gpt-4o")
	require.Contains(t, jsonText, "claude")
	require.NotContains(t, jsonText, "must-not-persist")
	require.NotContains(t, jsonText, "apiKey")
	require.NotContains(t, jsonText, "defaultModel")
	require.NotContains(t, jsonText, "\"provider\":")
	require.NotContains(t, jsonText, "extra")
}

func TestPatchCreativeModelPreferencePreservesNestedSafeSelectionSchema(t *testing.T) {
	setupCreativeModelTestDB(t)

	updatedAt := int64(1700000000100)
	created, conflict, err := PatchCreativeModelPreference(9, 0, CreativeModelPreferenceValue{
		Default: map[string]CreativeModelSafeSelection{
			"text": {
				ModelId:        "gpt-4o",
				ProfileId:      "profile-text",
				ProviderIdHint: "provider-hint",
				VendorHint:     "openai",
				UpdatedAt:      &updatedAt,
			},
		},
		Pinned:      []CreativeModelSafeSelection{{ModelId: "gpt-4o", ProfileId: "profile-text"}},
		Recent:      []CreativeModelSafeSelection{{ModelId: "claude-3-5-sonnet", VendorHint: "anthropic"}},
		DisplayMode: "compact",
		CustomOrder: []string{"claude-3-5-sonnet", "gpt-4o"},
	})
	require.NoError(t, err)
	require.False(t, conflict)
	require.Equal(t, 1, created.Revision)

	stored, exists, err := GetCreativeModelPreference(9)
	require.NoError(t, err)
	require.True(t, exists)
	roundTripped, err := CreativeModelPreferenceValueFromJSON(stored.PreferenceJSON)
	require.NoError(t, err)
	require.Equal(t, "gpt-4o", roundTripped.Default["text"].ModelId)
	require.Equal(t, "profile-text", roundTripped.Default["text"].ProfileId)
	require.Equal(t, "provider-hint", roundTripped.Default["text"].ProviderIdHint)
	require.Equal(t, "openai", roundTripped.Default["text"].VendorHint)
	require.NotNil(t, roundTripped.Default["text"].UpdatedAt)
	require.Equal(t, updatedAt, *roundTripped.Default["text"].UpdatedAt)
	require.Equal(t, []CreativeModelSafeSelection{{ModelId: "gpt-4o", ProfileId: "profile-text"}}, roundTripped.Pinned)
	require.Equal(t, []CreativeModelSafeSelection{{ModelId: "claude-3-5-sonnet", VendorHint: "anthropic"}}, roundTripped.Recent)
	require.Equal(t, "compact", roundTripped.DisplayMode)
	require.Equal(t, []string{"claude-3-5-sonnet", "gpt-4o"}, roundTripped.CustomOrder)
}

func TestCreativeModelPreferenceValueFromJSONUpgradesLegacyStringLists(t *testing.T) {
	roundTripped, err := CreativeModelPreferenceValueFromJSON(`{
		"default": "gpt-4o",
		"pinned": ["gpt-4o", "claude-3-5-sonnet"],
		"recent": ["claude-3-5-sonnet"],
		"displayMode": "compact",
		"customOrder": ["claude-3-5-sonnet", "gpt-4o"]
	}`)
	require.NoError(t, err)

	require.Equal(t, "gpt-4o", roundTripped.Default["text"].ModelId)
	require.Equal(t, []CreativeModelSafeSelection{{ModelId: "gpt-4o"}, {ModelId: "claude-3-5-sonnet"}}, roundTripped.Pinned)
	require.Equal(t, []CreativeModelSafeSelection{{ModelId: "claude-3-5-sonnet"}}, roundTripped.Recent)
	require.Equal(t, "compact", roundTripped.DisplayMode)
	require.Equal(t, []string{"claude-3-5-sonnet", "gpt-4o"}, roundTripped.CustomOrder)
}

func TestCreativeDocumentUpdateRevisionConflictAndIdempotency(t *testing.T) {
	setupCreativeModelTestDB(t)

	created, err := CreateCreativeDocument(&CreativeDocument{
		UserId:           11,
		DocumentId:       "doc-1",
		Title:            "First",
		SnapshotJSON:     "{\"nodes\":[]}",
		MetadataJSON:     "{}",
		ClientMutationId: "create-1",
	})
	require.NoError(t, err)
	require.Equal(t, 1, created.Revision)

	updated, conflict, err := UpdateCreativeDocumentSnapshot(11, "doc-1", 1, CreativeDocumentPatch{
		Title:            stringPtr("Second"),
		SnapshotJSON:     stringPtr("{\"nodes\":[1]}"),
		MetadataJSON:     stringPtr("{\"color\":\"blue\"}"),
		ClientMutationId: "update-1",
	})
	require.NoError(t, err)
	require.False(t, conflict)
	require.Equal(t, 2, updated.Revision)
	require.Equal(t, "Second", updated.Title)

	repeated, conflict, err := UpdateCreativeDocumentSnapshot(11, "doc-1", 1, CreativeDocumentPatch{
		Title:            stringPtr("Second"),
		SnapshotJSON:     stringPtr("{\"nodes\":[1]}"),
		MetadataJSON:     stringPtr("{\"color\":\"blue\"}"),
		ClientMutationId: "update-1",
	})
	require.NoError(t, err)
	require.False(t, conflict)
	require.Equal(t, 2, repeated.Revision)

	stale, conflict, err := UpdateCreativeDocumentSnapshot(11, "doc-1", 1, CreativeDocumentPatch{
		Title:            stringPtr("Stale"),
		SnapshotJSON:     stringPtr("{\"nodes\":[2]}"),
		ClientMutationId: "update-2",
	})
	require.NoError(t, err)
	require.True(t, conflict)
	require.Equal(t, 2, stale.Revision)
	require.Equal(t, "Second", stale.Title)
}

func stringPtr(value string) *string {
	return &value
}

func int64Ptr(value int64) *int64 {
	return &value
}
