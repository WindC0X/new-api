package model

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCreativeAssetMigrationAndOwnerHashUniqueness(t *testing.T) {
	setupCreativeModelTestDB(t)

	require.NoError(t, DB.AutoMigrate(&CreativeAsset{}, &CreativeAssetQuota{}, &CreativeDocumentAssetRef{}))

	first := &CreativeAsset{
		UserId:         101,
		AssetId:        "asset_owner_hash_1",
		ContentHash:    "hash-same-owner",
		MediaType:      "image",
		MimeType:       "image/png",
		SizeBytes:      3,
		StorageBackend: CreativeAssetStorageDatabase,
		Data:           []byte("png"),
	}
	require.NoError(t, DB.Create(first).Error)

	duplicateSameOwner := &CreativeAsset{
		UserId:         101,
		AssetId:        "asset_owner_hash_2",
		ContentHash:    "hash-same-owner",
		MediaType:      "image",
		MimeType:       "image/png",
		SizeBytes:      3,
		StorageBackend: CreativeAssetStorageDatabase,
		Data:           []byte("png"),
	}
	require.Error(t, DB.Create(duplicateSameOwner).Error)

	sameHashDifferentOwner := &CreativeAsset{
		UserId:         202,
		AssetId:        "asset_owner_hash_3",
		ContentHash:    "hash-same-owner",
		MediaType:      "image",
		MimeType:       "image/png",
		SizeBytes:      3,
		StorageBackend: CreativeAssetStorageS3Compatible,
		ObjectKey:      "creative-assets/u/opaque/asset_owner_hash_3/random",
		ObjectETag:     "etag",
		ObjectVersion:  "version",
	}
	require.NoError(t, DB.Create(sameHashDifferentOwner).Error)

	var stored CreativeAsset
	require.NoError(t, DB.Where("user_id = ? AND asset_id = ?", 202, "asset_owner_hash_3").First(&stored).Error)
	require.Empty(t, stored.Data, "S3-compatible assets must not persist DB bytes")
	require.Equal(t, CreativeAssetStorageS3Compatible, stored.StorageBackend)
	require.NotEmpty(t, stored.ObjectKey)
	require.NotEmpty(t, stored.ObjectETag)
	require.NotEmpty(t, stored.ObjectVersion)
}

func TestCreativeDocumentAssetRefsRefreshesSanitizedSnapshots(t *testing.T) {
	setupCreativeModelTestDB(t)
	require.NoError(t, DB.AutoMigrate(&CreativeAsset{}, &CreativeAssetQuota{}, &CreativeDocumentAssetRef{}))

	require.NoError(t, DB.Create(&CreativeAsset{
		UserId:         301,
		AssetId:        "asset_ref_keep_123456",
		ContentHash:    "hash-ref-keep",
		MediaType:      "image",
		MimeType:       "image/png",
		SizeBytes:      3,
		StorageBackend: CreativeAssetStorageDatabase,
		Data:           []byte("png"),
	}).Error)
	require.NoError(t, DB.Create(&CreativeAsset{
		UserId:         301,
		AssetId:        "asset_ref_audio_123456",
		ContentHash:    "hash-ref-audio",
		MediaType:      "audio",
		MimeType:       "audio/mpeg",
		SizeBytes:      4,
		StorageBackend: CreativeAssetStorageDatabase,
		Data:           []byte("mpeg"),
	}).Error)

	snapshot := `{"elements":[{"imageUrl":"/creative/api/assets/asset_ref_keep_123456/content"},{"clips":[{"audioUrl":"https://example.com/creative/api/assets/asset_ref_audio_123456/content"}]}]}`
	metadata := `{"thumbnailUrl":"/creative/api/assets/asset_ref_keep_123456/content"}`

	ids, err := ValidateCreativeDocumentAssetRefs(301, snapshot, metadata, "https://example.com")
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"asset_ref_keep_123456", "asset_ref_audio_123456"}, ids)
	require.NoError(t, RefreshCreativeDocumentAssetRefs(301, "doc-ref", ids))

	refs, err := ListCreativeDocumentAssetRefs(301, "doc-ref")
	require.NoError(t, err)
	require.Len(t, refs, 2)

	_, err = ValidateCreativeDocumentAssetRefs(301, `{"imageUrl":"/creative/api/assets/asset_missing_123456/content"}`, `{}`, "https://example.com")
	require.Error(t, err)
	require.Contains(t, err.Error(), "creative asset reference is invalid")

	_, err = ValidateCreativeDocumentAssetRefs(301, `{"imageUrl":"https://evil.example/creative/api/assets/asset_ref_keep_123456/content"}`, `{}`, "https://example.com")
	require.Error(t, err)
	require.Contains(t, err.Error(), "creative asset URL origin is invalid")
}
