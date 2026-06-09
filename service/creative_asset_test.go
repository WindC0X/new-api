package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupCreativeAssetServiceTestDB(t *testing.T) {
	t.Helper()

	originalDB := model.DB
	originalUsingSQLite := common.UsingSQLite
	originalUsingMySQL := common.UsingMySQL
	originalUsingPostgreSQL := common.UsingPostgreSQL
	originalRedisEnabled := common.RedisEnabled

	common.UsingSQLite = true
	common.UsingMySQL = false
	common.UsingPostgreSQL = false
	common.RedisEnabled = false

	db, err := gorm.Open(sqlite.Open("file:"+strings.ReplaceAll(t.Name(), "/", "_")+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	model.DB = db
	require.NoError(t, db.AutoMigrate(&model.CreativeAsset{}, &model.CreativeDocumentAssetRef{}))

	t.Cleanup(func() {
		sqlDB, err := db.DB()
		if err == nil {
			_ = sqlDB.Close()
		}
		model.DB = originalDB
		common.UsingSQLite = originalUsingSQLite
		common.UsingMySQL = originalUsingMySQL
		common.UsingPostgreSQL = originalUsingPostgreSQL
		common.RedisEnabled = originalRedisEnabled
	})
}

func TestCreativeAssetConfigFailsClosedForProductionWithoutS3(t *testing.T) {
	cfg := CreativeAssetConfig{
		Enabled:        true,
		RolloutMode:    CreativeAssetRolloutProduction,
		StorageBackend: model.CreativeAssetStorageDatabase,
	}
	_, err := NewCreativeAssetRuntime(cfg, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "production creative assets require s3-compatible storage")

	cfg.StorageBackend = model.CreativeAssetStorageS3Compatible
	cfg.S3Bucket = "private-bucket"
	cfg.S3Region = "auto"
	cfg.S3Endpoint = "https://s3.example"
	cfg.S3AccessKeyID = "test-ak"
	cfg.S3SecretAccessKey = ""
	_, err = NewCreativeAssetRuntime(cfg, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "s3-compatible storage is not configured")
}

func TestCreativeAssetDatabaseFallbackRequiresCanaryCapsAndDiskHeadroom(t *testing.T) {
	setupCreativeAssetServiceTestDB(t)

	cfg := CreativeAssetConfig{
		Enabled:                    true,
		RolloutMode:                CreativeAssetRolloutCanary,
		StorageBackend:             model.CreativeAssetStorageDatabase,
		DatabaseCanaryEnabled:      true,
		DatabaseGlobalMaxBytes:     64,
		DatabaseUserMaxBytes:       64,
		DatabaseReservedFreeBytes:  4,
		DatabaseDiskKillSwitch:     false,
		UserMaxBytes:               64,
		UserMaxAssets:              10,
		DiskSpaceProviderForWrites: func() common.DiskSpaceInfo { return common.DiskSpaceInfo{Total: 100, Free: 3, UsedPercent: 97} },
	}
	runtime, err := NewCreativeAssetRuntime(cfg, nil)
	require.NoError(t, err)
	_, _, err = runtime.CreateOrGet(context.Background(), 401, CreativeAssetCreateRequest{
		Reader:          bytes.NewReader([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 1}),
		Size:            9,
		ClientMimeType:  "image/png",
		ClientMediaType: "image",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "creative asset database storage is not healthy")

	cfg.DiskSpaceProviderForWrites = func() common.DiskSpaceInfo { return common.DiskSpaceInfo{Total: 100, Free: 50, UsedPercent: 50} }
	cfg.DatabaseGlobalMaxBytes = 64
	cfg.DatabaseUserMaxBytes = 64
	cfg.UserMaxBytes = 64
	runtime, err = NewCreativeAssetRuntime(cfg, nil)
	require.NoError(t, err)
	asset, duplicate, err := runtime.CreateOrGet(context.Background(), 401, CreativeAssetCreateRequest{
		Reader:          bytes.NewReader([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 1}),
		Size:            9,
		ClientMimeType:  "image/png",
		ClientMediaType: "image",
	})
	require.NoError(t, err)
	require.False(t, duplicate)
	require.Equal(t, model.CreativeAssetStorageDatabase, asset.StorageBackend)
	require.NotEmpty(t, asset.Data)
}

func TestCreativeAssetCreateDeduplicatesAndDoesNotLeakObjectKey(t *testing.T) {
	setupCreativeAssetServiceTestDB(t)
	runtime := mustCreativeAssetRuntimeForTest(t, CreativeAssetConfig{
		Enabled:                   true,
		RolloutMode:               CreativeAssetRolloutLocal,
		StorageBackend:            model.CreativeAssetStorageDatabase,
		DatabaseGlobalMaxBytes:    1024,
		DatabaseUserMaxBytes:      1024,
		DatabaseReservedFreeBytes: 0,
		UserMaxBytes:              1024,
		UserMaxAssets:             10,
		DiskSpaceProviderForWrites: func() common.DiskSpaceInfo {
			return common.DiskSpaceInfo{Total: 1024, Free: 1024, UsedPercent: 1}
		},
	}, nil)

	png := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 1, 2, 3}
	first, duplicate, err := runtime.CreateOrGet(context.Background(), 501, CreativeAssetCreateRequest{
		Reader:          bytes.NewReader(png),
		Size:            int64(len(png)),
		ClientMimeType:  "image/png",
		ClientMediaType: "image",
	})
	require.NoError(t, err)
	require.False(t, duplicate)

	second, duplicate, err := runtime.CreateOrGet(context.Background(), 501, CreativeAssetCreateRequest{
		Reader:          bytes.NewReader(png),
		Size:            int64(len(png)),
		ClientMimeType:  "image/png",
		ClientMediaType: "image",
	})
	require.NoError(t, err)
	require.True(t, duplicate)
	require.Equal(t, first.AssetId, second.AssetId)

	response := CreativeAssetPublicResponse(second)
	require.Equal(t, "/creative/api/assets/"+first.AssetId+"/content", response.URL)
	encoded, err := common.Marshal(response)
	require.NoError(t, err)
	body := string(encoded)
	require.NotContains(t, body, "ObjectKey")
	require.NotContains(t, body, "StorageBackend")
	require.NotContains(t, body, "bucket")
	require.NotContains(t, body, "s3.example")
}

func TestCreativeAssetFakeS3StorageSupportsRangeAndDelete(t *testing.T) {
	setupCreativeAssetServiceTestDB(t)
	fakeS3 := NewFakeS3CompatibleObjectClient()
	runtime := mustCreativeAssetRuntimeForTest(t, CreativeAssetConfig{
		Enabled:           true,
		RolloutMode:       CreativeAssetRolloutProduction,
		StorageBackend:    model.CreativeAssetStorageS3Compatible,
		S3Endpoint:        "https://s3.example",
		S3Region:          "auto",
		S3Bucket:          "private-bucket",
		S3Prefix:          "creative-test",
		S3AccessKeyID:     "test-ak",
		S3SecretAccessKey: "test-sk",
		UserMaxBytes:      1024,
		UserMaxAssets:     10,
	}, fakeS3)

	mp4 := []byte{0, 0, 0, 24, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm', 1, 2, 3, 4, 5, 6}
	asset, duplicate, err := runtime.CreateOrGet(context.Background(), 601, CreativeAssetCreateRequest{
		Reader:          bytes.NewReader(mp4),
		Size:            int64(len(mp4)),
		ClientMimeType:  "video/mp4",
		ClientMediaType: "video",
	})
	require.NoError(t, err)
	require.False(t, duplicate)
	require.Equal(t, model.CreativeAssetStorageS3Compatible, asset.StorageBackend)
	require.NotEmpty(t, asset.ObjectKey)
	require.NotContains(t, asset.ObjectKey, "601")
	require.NotContains(t, asset.ObjectKey, asset.ContentHash)

	content, err := runtime.OpenContent(context.Background(), 601, asset.AssetId, "bytes=4-11")
	require.NoError(t, err)
	defer content.Body.Close()
	got, err := io.ReadAll(content.Body)
	require.NoError(t, err)
	require.Equal(t, mp4[4:12], got)
	require.Equal(t, int64(4), content.RangeStart)
	require.Equal(t, int64(11), content.RangeEnd)
	require.Equal(t, httpStatusPartialContent, content.StatusCode)

	require.NoError(t, runtime.DeleteIfUnreferenced(context.Background(), 601, asset.AssetId))
	_, err = runtime.Get(context.Background(), 601, asset.AssetId)
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrCreativeAssetNotFound))
}

func mustCreativeAssetRuntimeForTest(t *testing.T, cfg CreativeAssetConfig, client S3CompatibleObjectClient) *CreativeAssetRuntime {
	t.Helper()

	runtime, err := NewCreativeAssetRuntime(cfg, client)
	require.NoError(t, err)
	return runtime
}
