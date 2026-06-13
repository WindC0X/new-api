package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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
	require.NoError(t, db.AutoMigrate(&model.CreativeAsset{}, &model.CreativeAssetQuota{}, &model.CreativeDocumentAssetRef{}, &model.CreativeAssetLifecycleOutbox{}))

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

func TestCreativeAssetS3ClientUsesManagedRedirectPolicy(t *testing.T) {
	previous := httpClient
	httpClient = nil
	t.Cleanup(func() { httpClient = previous })

	client := NewHTTPS3CompatibleObjectClient(CreativeAssetConfig{
		S3Endpoint:        "https://s3.example",
		S3Region:          "auto",
		S3Bucket:          "private-bucket",
		S3AccessKeyID:     "test-ak",
		S3SecretAccessKey: "test-sk",
	})

	require.NotNil(t, client.httpClient)
	require.NotSame(t, http.DefaultClient, client.httpClient)
	require.NotNil(t, client.httpClient.CheckRedirect)
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

func TestCreativeAssetConcurrentUploadsCannotBypassUserAssetQuota(t *testing.T) {
	setupCreativeAssetServiceTestDB(t)

	const workers = 3
	storage := newBarrierCreativeAssetStorage(workers)
	runtime := &CreativeAssetRuntime{
		cfg: normalizeCreativeAssetConfig(CreativeAssetConfig{
			Enabled:                   true,
			RolloutMode:               CreativeAssetRolloutLocal,
			StorageBackend:            model.CreativeAssetStorageDatabase,
			DatabaseGlobalMaxBytes:    1024,
			DatabaseUserMaxBytes:      1024,
			DatabaseReservedFreeBytes: 0,
			UserMaxBytes:              1024,
			UserMaxAssets:             1,
			DiskSpaceProviderForWrites: func() common.DiskSpaceInfo {
				return common.DiskSpaceInfo{Total: 1024, Free: 1024, UsedPercent: 1}
			},
		}),
		storage: storage,
	}

	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			payload := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', byte(i), byte(i + 1), byte(i + 2)}
			_, _, err := runtime.CreateOrGet(context.Background(), 701, CreativeAssetCreateRequest{
				Reader:          bytes.NewReader(payload),
				Size:            int64(len(payload)),
				ClientMimeType:  "image/png",
				ClientMediaType: "image",
			})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)

	successes := 0
	quotaFailures := 0
	for err := range errs {
		if err == nil {
			successes++
			continue
		}
		if errors.Is(err, ErrCreativeAssetQuotaExceeded) {
			quotaFailures++
		} else {
			require.NoError(t, err)
		}
	}
	require.Equal(t, 1, successes)
	require.Equal(t, workers-1, quotaFailures)

	count, err := model.CountCreativeAssets(701)
	require.NoError(t, err)
	require.EqualValues(t, 1, count)
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

func TestCreativeAssetDeleteFailureKeepsMetadataRetryable(t *testing.T) {
	setupCreativeAssetServiceTestDB(t)
	fakeS3 := NewFakeS3CompatibleObjectClient()
	failDelete := &failingDeleteS3Client{FakeS3CompatibleObjectClient: fakeS3, deleteErr: errors.New("delete temporarily failed")}
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
	}, failDelete)

	mp4 := []byte{0, 0, 0, 24, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm', 1, 2, 3, 4, 5, 6}
	asset, duplicate, err := runtime.CreateOrGet(context.Background(), 801, CreativeAssetCreateRequest{
		Reader:          bytes.NewReader(mp4),
		Size:            int64(len(mp4)),
		ClientMimeType:  "video/mp4",
		ClientMediaType: "video",
	})
	require.NoError(t, err)
	require.False(t, duplicate)

	err = runtime.DeleteIfUnreferenced(context.Background(), 801, asset.AssetId)
	require.Error(t, err)
	require.Contains(t, err.Error(), "delete temporarily failed")

	var stored model.CreativeAsset
	require.NoError(t, model.DB.Where("user_id = ? AND asset_id = ?", 801, asset.AssetId).First(&stored).Error)
	require.NotEmpty(t, stored.ObjectKey)

	failDelete.deleteErr = nil
	processed := runtime.ProcessPendingDeletes(context.Background(), 10)
	require.Equal(t, 1, processed)
	var count int64
	require.NoError(t, model.DB.Model(&model.CreativeAsset{}).Where("user_id = ? AND asset_id = ?", 801, asset.AssetId).Count(&count).Error)
	require.Zero(t, count)
}

func TestCreativeAssetLifecycleOutboxDeletesOrphanS3Upload(t *testing.T) {
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

	key := "creative-test/orphan-upload"
	_, err := fakeS3.PutObject(context.Background(), key, bytes.NewReader([]byte("orphan")), int64(len("orphan")), "image/png")
	require.NoError(t, err)

	orphan := &model.CreativeAsset{
		UserId:         901,
		AssetId:        "asset_orphan_upload",
		StorageBackend: model.CreativeAssetStorageS3Compatible,
		ObjectKey:      key,
	}
	_, err = model.EnqueueCreativeAssetLifecycleOutbox(orphan, model.CreativeAssetLifecycleOperationUploadCleanup)
	require.NoError(t, err)

	processed := runtime.ProcessPendingLifecycleOutboxes(context.Background(), 10)
	require.Equal(t, 1, processed)
	_, err = fakeS3.HeadObject(context.Background(), key)
	require.ErrorIs(t, err, ErrCreativeAssetNotFound)

	var outbox model.CreativeAssetLifecycleOutbox
	require.NoError(t, model.DB.First(&outbox).Error)
	require.Equal(t, model.CreativeAssetLifecycleStatusDone, outbox.Status)
}

func mustCreativeAssetRuntimeForTest(t *testing.T, cfg CreativeAssetConfig, client S3CompatibleObjectClient) *CreativeAssetRuntime {
	t.Helper()

	runtime, err := NewCreativeAssetRuntime(cfg, client)
	require.NoError(t, err)
	return runtime
}

type barrierCreativeAssetStorage struct {
	target   int32
	reached  int32
	released chan struct{}
	once     sync.Once
	order    int32
}

func newBarrierCreativeAssetStorage(target int) *barrierCreativeAssetStorage {
	return &barrierCreativeAssetStorage{target: int32(target), released: make(chan struct{})}
}

func (storage *barrierCreativeAssetStorage) Backend() string {
	return model.CreativeAssetStorageDatabase
}

func (storage *barrierCreativeAssetStorage) Store(ctx context.Context, asset *model.CreativeAsset, data []byte) (CreativeAssetObjectInfo, error) {
	if atomic.AddInt32(&storage.reached, 1) == storage.target {
		storage.once.Do(func() { close(storage.released) })
	}
	select {
	case <-storage.released:
	case <-time.After(2 * time.Second):
		return CreativeAssetObjectInfo{}, errors.New("timed out waiting for concurrent stores")
	}
	order := atomic.AddInt32(&storage.order, 1)
	time.Sleep(time.Duration(order-1) * 25 * time.Millisecond)
	return CreativeAssetObjectInfo{Size: int64(len(data))}, nil
}

func (storage *barrierCreativeAssetStorage) OpenRange(ctx context.Context, asset *model.CreativeAsset, rangeHeader string) (*CreativeAssetContent, error) {
	return nil, ErrCreativeAssetNotFound
}

func (storage *barrierCreativeAssetStorage) Head(ctx context.Context, asset *model.CreativeAsset) (CreativeAssetObjectInfo, error) {
	return CreativeAssetObjectInfo{}, nil
}

func (storage *barrierCreativeAssetStorage) Delete(ctx context.Context, asset *model.CreativeAsset) error {
	return nil
}

type failingDeleteS3Client struct {
	*FakeS3CompatibleObjectClient
	deleteErr error
}

func (client *failingDeleteS3Client) DeleteObject(ctx context.Context, key string) error {
	if client.deleteErr != nil {
		return client.deleteErr
	}
	return client.FakeS3CompatibleObjectClient.DeleteObject(ctx, key)
}
