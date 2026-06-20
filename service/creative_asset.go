package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsv4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"gorm.io/gorm"
)

const (
	CreativeAssetRolloutLocal      = "local"
	CreativeAssetRolloutCanary     = "canary"
	CreativeAssetRolloutProduction = "production"

	CreativeAssetMaxBytes        int64 = 64 << 20
	CreativeAssetDefaultUserMax  int64 = 2 << 30
	CreativeAssetDefaultMaxCount int64 = 10000

	creativeAssetDefaultDBGlobalMaxBytes    int64 = 1 << 30
	creativeAssetDefaultDBUserMaxBytes      int64 = 256 << 20
	creativeAssetDefaultDBReservedFreeBytes int64 = 4 << 30
	creativeAssetDefaultS3RequestTimeoutSec       = 30

	httpStatusPartialContent = http.StatusPartialContent
)

var (
	ErrCreativeAssetDisabled            = errors.New("creative asset sync is disabled")
	ErrCreativeAssetNotFound            = errors.New("creative asset not found")
	ErrCreativeAssetQuotaExceeded       = model.ErrCreativeAssetQuotaExceeded
	ErrCreativeAssetTooLarge            = errors.New("creative asset is too large")
	ErrCreativeAssetInvalid             = errors.New("creative asset is invalid")
	ErrCreativeAssetRangeNotSatisfiable = errors.New("creative asset range is not satisfiable")
	ErrCreativeAssetReferenced          = model.ErrCreativeAssetReferenced
)

type CreativeAssetConfig struct {
	Enabled                   bool
	RolloutMode               string
	StorageBackend            string
	DatabaseCanaryEnabled     bool
	DatabaseGlobalMaxBytes    int64
	DatabaseUserMaxBytes      int64
	DatabaseReservedFreeBytes int64
	DatabaseDiskKillSwitch    bool
	UserMaxBytes              int64
	UserMaxAssets             int64
	S3Endpoint                string
	S3Region                  string
	S3Bucket                  string
	S3Prefix                  string
	S3AccessKeyID             string
	S3SecretAccessKey         string
	S3ForcePathStyle          bool
	S3RequestTimeoutSeconds   int

	DiskSpaceProviderForWrites func() common.DiskSpaceInfo
}

type CreativeAssetCreateRequest struct {
	Reader          io.Reader
	Size            int64
	ClientMimeType  string
	ClientMediaType string
	Metadata        map[string]string
}

type CreativeAssetObjectInfo struct {
	ETag    string
	Version string
	Size    int64
}

type CreativeAssetContent struct {
	Body          io.ReadCloser
	Size          int64
	MimeType      string
	MediaType     string
	StatusCode    int
	RangeStart    int64
	RangeEnd      int64
	ContentRange  string
	AcceptRanges  bool
	StorageETag   string
	StorageObject string
}

type CreativeAssetPublic struct {
	ID          string `json:"id"`
	ContentHash string `json:"contentHash"`
	MediaType   string `json:"mediaType"`
	MimeType    string `json:"mimeType"`
	Size        int64  `json:"size"`
	URL         string `json:"url"`
	CreatedTime int64  `json:"createdTime"`
	UpdatedTime int64  `json:"updatedTime"`
}

type CreativeAssetStorage interface {
	Backend() string
	Store(ctx context.Context, asset *model.CreativeAsset, data []byte) (CreativeAssetObjectInfo, error)
	OpenRange(ctx context.Context, asset *model.CreativeAsset, rangeHeader string) (*CreativeAssetContent, error)
	Head(ctx context.Context, asset *model.CreativeAsset) (CreativeAssetObjectInfo, error)
	Delete(ctx context.Context, asset *model.CreativeAsset) error
}

type S3CompatibleObjectClient interface {
	PutObject(ctx context.Context, key string, body io.Reader, size int64, mimeType string) (CreativeAssetObjectInfo, error)
	GetObject(ctx context.Context, key string, start int64, end int64) (io.ReadCloser, CreativeAssetObjectInfo, error)
	HeadObject(ctx context.Context, key string) (CreativeAssetObjectInfo, error)
	DeleteObject(ctx context.Context, key string) error
}

type CreativeAssetRuntime struct {
	cfg      CreativeAssetConfig
	storage  CreativeAssetStorage
	disabled string
}

var (
	creativeAssetRuntimeMu sync.RWMutex
	creativeAssetRuntime   *CreativeAssetRuntime
)

func DefaultCreativeAssetConfigFromEnv() CreativeAssetConfig {
	cfg := CreativeAssetConfig{
		Enabled:                   common.GetEnvOrDefaultBool("CREATIVE_ASSET_SYNC_ENABLED", false),
		RolloutMode:               strings.TrimSpace(os.Getenv("CREATIVE_ASSET_ROLLOUT_MODE")),
		StorageBackend:            strings.TrimSpace(common.GetEnvOrDefaultString("CREATIVE_ASSET_STORAGE", model.CreativeAssetStorageDatabase)),
		DatabaseCanaryEnabled:     common.GetEnvOrDefaultBool("CREATIVE_ASSET_DATABASE_CANARY_ENABLED", false),
		DatabaseGlobalMaxBytes:    envInt64("CREATIVE_ASSET_DB_GLOBAL_MAX_BYTES", creativeAssetDefaultDBGlobalMaxBytes),
		DatabaseUserMaxBytes:      envInt64("CREATIVE_ASSET_DB_USER_MAX_BYTES", creativeAssetDefaultDBUserMaxBytes),
		DatabaseReservedFreeBytes: envInt64("CREATIVE_ASSET_DB_RESERVED_FREE_BYTES", creativeAssetDefaultDBReservedFreeBytes),
		DatabaseDiskKillSwitch:    common.GetEnvOrDefaultBool("CREATIVE_ASSET_DATABASE_KILL_SWITCH", false),
		UserMaxBytes:              envInt64("CREATIVE_ASSET_USER_MAX_BYTES", CreativeAssetDefaultUserMax),
		UserMaxAssets:             envInt64("CREATIVE_ASSET_USER_MAX_ASSETS", CreativeAssetDefaultMaxCount),
		S3Endpoint:                strings.TrimSpace(os.Getenv("CREATIVE_ASSET_S3_ENDPOINT")),
		S3Region:                  strings.TrimSpace(os.Getenv("CREATIVE_ASSET_S3_REGION")),
		S3Bucket:                  strings.TrimSpace(os.Getenv("CREATIVE_ASSET_S3_BUCKET")),
		S3Prefix:                  strings.Trim(strings.TrimSpace(os.Getenv("CREATIVE_ASSET_S3_PREFIX")), "/"),
		S3AccessKeyID:             strings.TrimSpace(os.Getenv("CREATIVE_ASSET_S3_ACCESS_KEY_ID")),
		S3SecretAccessKey:         strings.TrimSpace(os.Getenv("CREATIVE_ASSET_S3_SECRET_ACCESS_KEY")),
		S3ForcePathStyle:          common.GetEnvOrDefaultBool("CREATIVE_ASSET_S3_FORCE_PATH_STYLE", false),
		S3RequestTimeoutSeconds:   common.GetEnvOrDefault("CREATIVE_ASSET_S3_REQUEST_TIMEOUT_SECONDS", creativeAssetDefaultS3RequestTimeoutSec),
		DiskSpaceProviderForWrites: func() common.DiskSpaceInfo {
			return common.GetDiskSpaceInfo()
		},
	}
	return cfg
}

func CurrentCreativeAssetRuntime() *CreativeAssetRuntime {
	creativeAssetRuntimeMu.RLock()
	runtime := creativeAssetRuntime
	creativeAssetRuntimeMu.RUnlock()
	if runtime != nil {
		return runtime
	}
	cfg := DefaultCreativeAssetConfigFromEnv()
	created, err := NewCreativeAssetRuntime(cfg, nil)
	if err != nil {
		created = &CreativeAssetRuntime{cfg: cfg, disabled: scrubCreativeAssetError(err).Error()}
	}
	creativeAssetRuntimeMu.Lock()
	if creativeAssetRuntime == nil {
		creativeAssetRuntime = created
	}
	runtime = creativeAssetRuntime
	creativeAssetRuntimeMu.Unlock()
	return runtime
}

func InitializeCreativeAssetRuntimeFromEnv() (*CreativeAssetRuntime, error) {
	cfg := DefaultCreativeAssetConfigFromEnv()
	runtime, err := NewCreativeAssetRuntime(cfg, nil)
	if err != nil {
		return nil, scrubCreativeAssetError(err)
	}
	creativeAssetRuntimeMu.Lock()
	creativeAssetRuntime = runtime
	creativeAssetRuntimeMu.Unlock()
	return runtime, nil
}

func SetCreativeAssetRuntimeForTest(t interface{ Cleanup(func()) }, runtime *CreativeAssetRuntime) {
	creativeAssetRuntimeMu.Lock()
	old := creativeAssetRuntime
	creativeAssetRuntime = runtime
	creativeAssetRuntimeMu.Unlock()
	t.Cleanup(func() {
		creativeAssetRuntimeMu.Lock()
		creativeAssetRuntime = old
		creativeAssetRuntimeMu.Unlock()
	})
}

func NewCreativeAssetRuntime(cfg CreativeAssetConfig, s3Client S3CompatibleObjectClient) (*CreativeAssetRuntime, error) {
	rawRolloutMode := strings.TrimSpace(cfg.RolloutMode)
	cfg = normalizeCreativeAssetConfig(cfg)
	if !cfg.Enabled {
		return &CreativeAssetRuntime{cfg: cfg, disabled: "creative asset sync disabled"}, nil
	}
	if rawRolloutMode == "" {
		return nil, errors.New("creative asset rollout mode must be explicit when sync is enabled")
	}
	if !creativeAssetRolloutModeAllowed(cfg.RolloutMode) {
		return nil, fmt.Errorf("unsupported creative asset rollout mode: %s", cfg.RolloutMode)
	}
	if cfg.RolloutMode == CreativeAssetRolloutProduction && cfg.StorageBackend != model.CreativeAssetStorageS3Compatible {
		return nil, errors.New("production creative assets require s3-compatible storage")
	}

	var storage CreativeAssetStorage
	switch cfg.StorageBackend {
	case model.CreativeAssetStorageDatabase:
		if cfg.RolloutMode == CreativeAssetRolloutProduction {
			return nil, errors.New("production creative assets require s3-compatible storage")
		}
		if cfg.RolloutMode == CreativeAssetRolloutCanary && !cfg.DatabaseCanaryEnabled {
			return nil, errors.New("creative asset database canary is not enabled")
		}
		if cfg.RolloutMode == CreativeAssetRolloutCanary {
			if cfg.DatabaseGlobalMaxBytes > creativeAssetDefaultDBGlobalMaxBytes || cfg.DatabaseUserMaxBytes > creativeAssetDefaultDBUserMaxBytes {
				return nil, errors.New("creative asset database canary caps are too high")
			}
		}
		storage = &DatabaseCreativeAssetStorage{cfg: cfg}
	case model.CreativeAssetStorageS3Compatible:
		if !creativeS3ConfigComplete(cfg) {
			return nil, errors.New("s3-compatible storage is not configured")
		}
		if cfg.RolloutMode == CreativeAssetRolloutProduction && !creativeS3EndpointHTTPS(cfg.S3Endpoint) {
			return nil, errors.New("production creative assets require an HTTPS s3-compatible endpoint")
		}
		if s3Client == nil {
			s3Client = NewHTTPS3CompatibleObjectClient(cfg)
		}
		storage = &S3CompatibleCreativeAssetStorage{cfg: cfg, client: s3Client}
	default:
		return nil, fmt.Errorf("unsupported creative asset storage backend: %s", cfg.StorageBackend)
	}
	return &CreativeAssetRuntime{cfg: cfg, storage: storage}, nil
}

func (runtime *CreativeAssetRuntime) Status() (bool, string) {
	if runtime == nil {
		return false, "creative asset runtime is not initialized"
	}
	if runtime.disabled != "" {
		return false, runtime.disabled
	}
	if runtime.storage == nil {
		return false, "creative asset storage is not configured"
	}
	return true, ""
}

func (runtime *CreativeAssetRuntime) CreateOrGet(ctx context.Context, userId int, req CreativeAssetCreateRequest) (*model.CreativeAsset, bool, error) {
	if err := runtime.ready(); err != nil {
		return nil, false, err
	}
	if err := rejectCreativeAssetMetadata(req.Metadata); err != nil {
		return nil, false, err
	}
	if req.Reader == nil {
		return nil, false, fmt.Errorf("%w: file is required", ErrCreativeAssetInvalid)
	}
	if req.Size <= 0 {
		return nil, false, fmt.Errorf("%w: content length is required", ErrCreativeAssetInvalid)
	}
	if req.Size > CreativeAssetMaxBytes {
		return nil, false, ErrCreativeAssetTooLarge
	}

	data, err := readCreativeAssetBytes(req.Reader, CreativeAssetMaxBytes)
	if err != nil {
		return nil, false, err
	}
	if int64(len(data)) != req.Size {
		return nil, false, fmt.Errorf("%w: content length mismatch", ErrCreativeAssetInvalid)
	}

	mediaType, mimeType, err := detectCreativeAssetType(data, req.ClientMimeType, req.ClientMediaType)
	if err != nil {
		return nil, false, err
	}
	sum := sha256.Sum256(data)
	contentHash := hex.EncodeToString(sum[:])

	if existing, exists, err := model.GetCreativeAssetByContentHash(userId, contentHash); err != nil {
		return nil, false, err
	} else if exists {
		_ = model.TouchCreativeAssetAccessedTime(userId, existing.AssetId)
		return existing, true, nil
	}
	if err := runtime.checkQuotaAndStorageHealth(userId, int64(len(data))); err != nil {
		return nil, false, err
	}

	assetId, err := generateCreativeAssetId()
	if err != nil {
		return nil, false, err
	}
	now := time.Now().Unix()
	asset := &model.CreativeAsset{
		UserId:           userId,
		AssetId:          assetId,
		ContentHash:      contentHash,
		MediaType:        mediaType,
		MimeType:         mimeType,
		SizeBytes:        int64(len(data)),
		StorageBackend:   runtime.storage.Backend(),
		CreatedTime:      now,
		UpdatedTime:      now,
		LastAccessedTime: now,
	}
	if asset.StorageBackend == model.CreativeAssetStorageS3Compatible {
		asset.ObjectKey, err = runtime.generateObjectKey(userId, assetId)
		if err != nil {
			return nil, false, err
		}
	} else {
		asset.Data = data
	}

	info, err := runtime.storage.Store(ctx, asset, data)
	if err != nil {
		return nil, false, scrubCreativeAssetError(err)
	}
	asset.ObjectETag = info.ETag
	asset.ObjectVersion = info.Version

	created, duplicate, err := model.CreateCreativeAssetWithQuota(asset, model.CreativeAssetQuotaLimits{
		MaxAssets:              runtime.cfg.UserMaxAssets,
		MaxBytes:               runtime.cfg.UserMaxBytes,
		DatabaseUserMaxBytes:   runtime.cfg.DatabaseUserMaxBytes,
		DatabaseGlobalMaxBytes: runtime.cfg.DatabaseGlobalMaxBytes,
		EnforceDatabaseLimits:  asset.StorageBackend == model.CreativeAssetStorageDatabase,
	})
	if err != nil {
		if asset.StorageBackend == model.CreativeAssetStorageS3Compatible {
			runtime.cleanupStoredObjectOrEnqueue(context.Background(), asset)
		}
		if existing, exists, lookupErr := model.GetCreativeAssetByContentHash(userId, contentHash); lookupErr == nil && exists {
			return existing, true, nil
		}
		return nil, false, err
	}
	if duplicate {
		if asset.StorageBackend == model.CreativeAssetStorageS3Compatible {
			runtime.cleanupStoredObjectOrEnqueue(context.Background(), asset)
		}
		_ = model.TouchCreativeAssetAccessedTime(userId, created.AssetId)
		return created, true, nil
	}
	return created, false, nil
}

func (runtime *CreativeAssetRuntime) Get(ctx context.Context, userId int, assetId string) (*model.CreativeAsset, error) {
	if err := runtime.ready(); err != nil {
		return nil, err
	}
	if !model.IsValidCreativeAssetId(assetId) {
		return nil, ErrCreativeAssetNotFound
	}
	asset, exists, err := model.GetCreativeAsset(userId, assetId)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, ErrCreativeAssetNotFound
	}
	return asset, nil
}

func (runtime *CreativeAssetRuntime) OpenContent(ctx context.Context, userId int, assetId string, rangeHeader string) (*CreativeAssetContent, error) {
	asset, err := runtime.Get(ctx, userId, assetId)
	if err != nil {
		return nil, err
	}
	content, err := runtime.storage.OpenRange(ctx, asset, rangeHeader)
	if err != nil {
		return nil, err
	}
	content.MimeType = asset.MimeType
	content.MediaType = asset.MediaType
	_ = model.TouchCreativeAssetAccessedTime(userId, assetId)
	return content, nil
}

func (runtime *CreativeAssetRuntime) DeleteIfUnreferenced(ctx context.Context, userId int, assetId string) error {
	if err := runtime.ready(); err != nil {
		return err
	}
	asset, exists, err := model.MarkCreativeAssetPendingDelete(userId, assetId)
	if err != nil {
		return err
	}
	if !exists {
		return ErrCreativeAssetNotFound
	}
	asset, exists, err = model.ConfirmCreativeAssetPendingDeleteForStorage(userId, assetId)
	if err != nil {
		return err
	}
	if !exists {
		return ErrCreativeAssetNotFound
	}
	if err := runtime.storage.Delete(ctx, asset); err != nil {
		scrubbed := scrubCreativeAssetError(err)
		_ = model.MarkCreativeAssetDeleteFailed(userId, assetId, scrubbed)
		return scrubbed
	}
	return model.FinalizeCreativeAssetDelete(userId, assetId)
}

func (runtime *CreativeAssetRuntime) ProcessPendingLifecycleOutboxes(ctx context.Context, limit int) int {
	if err := runtime.ready(); err != nil {
		return 0
	}
	outboxes, err := model.ListPendingCreativeAssetLifecycleOutboxes(limit)
	if err != nil {
		common.SysError(fmt.Sprintf("list creative asset lifecycle outboxes failed: %v", scrubCreativeAssetError(err)))
		return 0
	}
	processed := 0
	for _, outbox := range outboxes {
		claimed, err := model.ClaimCreativeAssetLifecycleOutbox(outbox.ID)
		if err != nil {
			common.SysError(fmt.Sprintf("claim creative asset lifecycle outbox failed: %v", scrubCreativeAssetError(err)))
			continue
		}
		if !claimed {
			continue
		}
		if outbox.Operation != model.CreativeAssetLifecycleOperationUploadCleanup ||
			outbox.StorageBackend != model.CreativeAssetStorageS3Compatible ||
			outbox.StorageBackend != runtime.storage.Backend() {
			lifecycleErr := errors.New("creative asset lifecycle outbox storage backend or operation is unsupported")
			_ = model.MarkCreativeAssetLifecycleOutboxFailed(outbox.ID, lifecycleErr)
			continue
		}
		asset := &model.CreativeAsset{
			UserId:         outbox.UserId,
			AssetId:        outbox.AssetId,
			StorageBackend: outbox.StorageBackend,
			ObjectKey:      outbox.ObjectKey,
		}
		if err := runtime.storage.Delete(ctx, asset); err != nil {
			scrubbed := scrubCreativeAssetError(err)
			_ = model.MarkCreativeAssetLifecycleOutboxFailed(outbox.ID, scrubbed)
			continue
		}
		if err := model.MarkCreativeAssetLifecycleOutboxDone(outbox.ID); err != nil {
			common.SysError(fmt.Sprintf("mark creative asset lifecycle outbox done failed: %v", scrubCreativeAssetError(err)))
			continue
		}
		processed++
	}
	return processed
}

func (runtime *CreativeAssetRuntime) ProcessPendingDeletes(ctx context.Context, limit int) int {
	if err := runtime.ready(); err != nil {
		return 0
	}
	assets, err := model.ListPendingDeleteCreativeAssets(limit)
	if err != nil {
		common.SysError(fmt.Sprintf("list pending creative asset deletes failed: %v", scrubCreativeAssetError(err)))
		return 0
	}
	processed := 0
	for _, asset := range assets {
		if asset.StorageBackend != runtime.storage.Backend() {
			_ = model.MarkCreativeAssetDeleteFailed(asset.UserId, asset.AssetId, errors.New("creative asset storage backend is not configured for retry"))
			continue
		}
		confirmed, exists, err := model.ConfirmCreativeAssetPendingDeleteForStorage(asset.UserId, asset.AssetId)
		if err != nil {
			common.SysError(fmt.Sprintf("confirm pending creative asset delete failed: %v", scrubCreativeAssetError(err)))
			continue
		}
		if !exists {
			continue
		}
		if err := runtime.storage.Delete(ctx, confirmed); err != nil {
			scrubbed := scrubCreativeAssetError(err)
			_ = model.MarkCreativeAssetDeleteFailed(asset.UserId, asset.AssetId, scrubbed)
			continue
		}
		if err := model.FinalizeCreativeAssetDelete(asset.UserId, asset.AssetId); err != nil {
			common.SysError(fmt.Sprintf("finalize creative asset delete failed: %v", scrubCreativeAssetError(err)))
			continue
		}
		processed++
	}
	return processed
}

func (runtime *CreativeAssetRuntime) cleanupStoredObjectOrEnqueue(ctx context.Context, asset *model.CreativeAsset) {
	if runtime == nil || runtime.storage == nil || asset == nil || asset.StorageBackend != model.CreativeAssetStorageS3Compatible {
		return
	}
	if err := runtime.storage.Delete(ctx, asset); err == nil {
		return
	}
	if _, enqueueErr := model.EnqueueCreativeAssetLifecycleOutbox(asset, model.CreativeAssetLifecycleOperationUploadCleanup); enqueueErr != nil {
		common.SysError(fmt.Sprintf("enqueue creative asset lifecycle cleanup failed: %v", scrubCreativeAssetError(enqueueErr)))
	}
}

func (runtime *CreativeAssetRuntime) ready() error {
	if runtime == nil {
		return ErrCreativeAssetDisabled
	}
	if runtime.disabled != "" {
		return fmt.Errorf("%w: %s", ErrCreativeAssetDisabled, runtime.disabled)
	}
	if runtime.storage == nil {
		return fmt.Errorf("%w: storage is not configured", ErrCreativeAssetDisabled)
	}
	return nil
}

func (runtime *CreativeAssetRuntime) checkQuotaAndStorageHealth(userId int, incomingBytes int64) error {
	maxBytes := runtime.cfg.UserMaxBytes
	if maxBytes <= 0 {
		maxBytes = CreativeAssetDefaultUserMax
	}
	maxAssets := runtime.cfg.UserMaxAssets
	if maxAssets <= 0 {
		maxAssets = CreativeAssetDefaultMaxCount
	}
	count, err := model.CountCreativeAssets(userId)
	if err != nil {
		return err
	}
	if count >= maxAssets {
		return ErrCreativeAssetQuotaExceeded
	}
	total, err := model.SumCreativeAssetBytes(userId)
	if err != nil {
		return err
	}
	if total+incomingBytes > maxBytes {
		return ErrCreativeAssetQuotaExceeded
	}
	if runtime.cfg.StorageBackend == model.CreativeAssetStorageDatabase {
		if runtime.cfg.DatabaseDiskKillSwitch {
			return errors.New("creative asset database storage is disabled by kill switch")
		}
		userDBBytes, err := model.SumCreativeDatabaseAssetBytes(&userId)
		if err != nil {
			return err
		}
		if userDBBytes+incomingBytes > runtime.cfg.DatabaseUserMaxBytes {
			return ErrCreativeAssetQuotaExceeded
		}
		globalDBBytes, err := model.SumCreativeDatabaseAssetBytes(nil)
		if err != nil {
			return err
		}
		if globalDBBytes+incomingBytes > runtime.cfg.DatabaseGlobalMaxBytes {
			return ErrCreativeAssetQuotaExceeded
		}
		disk := runtime.cfg.DiskSpaceProviderForWrites()
		if disk.Total > 0 {
			reserve := runtime.cfg.DatabaseReservedFreeBytes
			if reserve < 0 {
				reserve = 0
			}
			if disk.Free < uint64(incomingBytes+reserve) || disk.UsedPercent >= 90 {
				return errors.New("creative asset database storage is not healthy")
			}
		}
	}
	return nil
}

func (runtime *CreativeAssetRuntime) generateObjectKey(userId int, assetId string) (string, error) {
	ownerSalt, err := common.GenerateRandomCharsKey(18)
	if err != nil {
		return "", err
	}
	randomTail, err := common.GenerateRandomCharsKey(24)
	if err != nil {
		return "", err
	}
	ownerOpaque := common.HmacSha256(fmt.Sprintf("%d:%s", userId, ownerSalt), randomTail)
	prefix := strings.Trim(strings.TrimSpace(runtime.cfg.S3Prefix), "/")
	parts := []string{"creative-assets", "u", ownerOpaque[:24], assetId, randomTail}
	if prefix != "" {
		parts = append([]string{prefix}, parts...)
	}
	return strings.Join(parts, "/"), nil
}

func CreativeAssetPublicResponse(asset *model.CreativeAsset) CreativeAssetPublic {
	if asset == nil {
		return CreativeAssetPublic{}
	}
	return CreativeAssetPublic{
		ID:          asset.AssetId,
		ContentHash: asset.ContentHash,
		MediaType:   asset.MediaType,
		MimeType:    asset.MimeType,
		Size:        asset.SizeBytes,
		URL:         "/creative/api/assets/" + asset.AssetId + "/content",
		CreatedTime: asset.CreatedTime,
		UpdatedTime: asset.UpdatedTime,
	}
}

type DatabaseCreativeAssetStorage struct {
	cfg CreativeAssetConfig
}

func (storage *DatabaseCreativeAssetStorage) Backend() string {
	return model.CreativeAssetStorageDatabase
}

func (storage *DatabaseCreativeAssetStorage) Store(ctx context.Context, asset *model.CreativeAsset, data []byte) (CreativeAssetObjectInfo, error) {
	if len(data) == 0 {
		return CreativeAssetObjectInfo{}, fmt.Errorf("%w: file is empty", ErrCreativeAssetInvalid)
	}
	asset.Data = data
	asset.ObjectKey = ""
	return CreativeAssetObjectInfo{Size: int64(len(data))}, nil
}

func (storage *DatabaseCreativeAssetStorage) OpenRange(ctx context.Context, asset *model.CreativeAsset, rangeHeader string) (*CreativeAssetContent, error) {
	if asset == nil || asset.StorageBackend != model.CreativeAssetStorageDatabase || len(asset.Data) == 0 {
		return nil, ErrCreativeAssetNotFound
	}
	start, end, status, contentRange, err := parseCreativeHTTPRange(rangeHeader, int64(len(asset.Data)))
	if err != nil {
		return nil, err
	}
	body := bytes.NewReader(asset.Data[start : end+1])
	return &CreativeAssetContent{
		Body:         io.NopCloser(body),
		Size:         int64(len(asset.Data)),
		StatusCode:   status,
		RangeStart:   start,
		RangeEnd:     end,
		ContentRange: contentRange,
		AcceptRanges: true,
	}, nil
}

func (storage *DatabaseCreativeAssetStorage) Head(ctx context.Context, asset *model.CreativeAsset) (CreativeAssetObjectInfo, error) {
	if asset == nil || asset.StorageBackend != model.CreativeAssetStorageDatabase {
		return CreativeAssetObjectInfo{}, ErrCreativeAssetNotFound
	}
	return CreativeAssetObjectInfo{Size: int64(len(asset.Data))}, nil
}

func (storage *DatabaseCreativeAssetStorage) Delete(ctx context.Context, asset *model.CreativeAsset) error {
	return nil
}

type S3CompatibleCreativeAssetStorage struct {
	cfg    CreativeAssetConfig
	client S3CompatibleObjectClient
}

func (storage *S3CompatibleCreativeAssetStorage) Backend() string {
	return model.CreativeAssetStorageS3Compatible
}

func (storage *S3CompatibleCreativeAssetStorage) Store(ctx context.Context, asset *model.CreativeAsset, data []byte) (CreativeAssetObjectInfo, error) {
	if asset == nil || strings.TrimSpace(asset.ObjectKey) == "" {
		return CreativeAssetObjectInfo{}, errors.New("creative asset object key is missing")
	}
	info, err := storage.client.PutObject(ctx, asset.ObjectKey, bytes.NewReader(data), int64(len(data)), asset.MimeType)
	if err != nil {
		return CreativeAssetObjectInfo{}, err
	}
	head, err := storage.client.HeadObject(ctx, asset.ObjectKey)
	if err != nil {
		return CreativeAssetObjectInfo{}, err
	}
	if head.Size != int64(len(data)) {
		_ = storage.client.DeleteObject(ctx, asset.ObjectKey)
		return CreativeAssetObjectInfo{}, errors.New("creative asset object verification failed")
	}
	if info.ETag == "" {
		info.ETag = head.ETag
	}
	if info.Version == "" {
		info.Version = head.Version
	}
	info.Size = head.Size
	return info, nil
}

func (storage *S3CompatibleCreativeAssetStorage) OpenRange(ctx context.Context, asset *model.CreativeAsset, rangeHeader string) (*CreativeAssetContent, error) {
	if asset == nil || strings.TrimSpace(asset.ObjectKey) == "" {
		return nil, ErrCreativeAssetNotFound
	}
	head, err := storage.client.HeadObject(ctx, asset.ObjectKey)
	if err != nil {
		return nil, err
	}
	start, end, status, contentRange, err := parseCreativeHTTPRange(rangeHeader, head.Size)
	if err != nil {
		return nil, err
	}
	body, info, err := storage.client.GetObject(ctx, asset.ObjectKey, start, end)
	if err != nil {
		return nil, err
	}
	return &CreativeAssetContent{
		Body:          body,
		Size:          head.Size,
		StatusCode:    status,
		RangeStart:    start,
		RangeEnd:      end,
		ContentRange:  contentRange,
		AcceptRanges:  true,
		StorageETag:   info.ETag,
		StorageObject: "s3-compatible",
	}, nil
}

func (storage *S3CompatibleCreativeAssetStorage) Head(ctx context.Context, asset *model.CreativeAsset) (CreativeAssetObjectInfo, error) {
	if asset == nil || strings.TrimSpace(asset.ObjectKey) == "" {
		return CreativeAssetObjectInfo{}, ErrCreativeAssetNotFound
	}
	return storage.client.HeadObject(ctx, asset.ObjectKey)
}

func (storage *S3CompatibleCreativeAssetStorage) Delete(ctx context.Context, asset *model.CreativeAsset) error {
	if asset == nil || strings.TrimSpace(asset.ObjectKey) == "" {
		return nil
	}
	return storage.client.DeleteObject(ctx, asset.ObjectKey)
}

type FakeS3CompatibleObjectClient struct {
	mu      sync.RWMutex
	objects map[string]fakeS3Object
}

type HTTPS3CompatibleObjectClient struct {
	cfg         CreativeAssetConfig
	httpClient  *http.Client
	signer      *awsv4.Signer
	credentials aws.Credentials
}

func NewHTTPS3CompatibleObjectClient(cfg CreativeAssetConfig) *HTTPS3CompatibleObjectClient {
	cfg = normalizeCreativeAssetConfig(cfg)
	return &HTTPS3CompatibleObjectClient{
		cfg:        cfg,
		httpClient: creativeAssetS3HTTPClient(ensureHTTPClient(), cfg.S3RequestTimeoutSeconds),
		signer:     awsv4.NewSigner(),
		credentials: aws.Credentials{
			AccessKeyID:     strings.TrimSpace(cfg.S3AccessKeyID),
			SecretAccessKey: strings.TrimSpace(cfg.S3SecretAccessKey),
			Source:          "creative-asset-s3-compatible-env",
		},
	}
}

func creativeAssetS3HTTPClient(base *http.Client, timeoutSeconds int) *http.Client {
	if base == nil {
		base = http.DefaultClient
	}
	clone := *base
	if timeoutSeconds <= 0 {
		timeoutSeconds = creativeAssetDefaultS3RequestTimeoutSec
	}
	clone.Timeout = time.Duration(timeoutSeconds) * time.Second
	if clone.CheckRedirect == nil {
		clone.CheckRedirect = checkRedirect
	}
	return &clone
}

func (client *HTTPS3CompatibleObjectClient) PutObject(ctx context.Context, key string, body io.Reader, size int64, mimeType string) (CreativeAssetObjectInfo, error) {
	data, err := io.ReadAll(body)
	if err != nil {
		return CreativeAssetObjectInfo{}, err
	}
	if int64(len(data)) != size {
		return CreativeAssetObjectInfo{}, errors.New("s3 put size mismatch")
	}
	sum := sha256.Sum256(data)
	payloadHash := hex.EncodeToString(sum[:])
	req, err := client.newSignedRequest(ctx, http.MethodPut, key, bytes.NewReader(data), size, payloadHash)
	if err != nil {
		return CreativeAssetObjectInfo{}, err
	}
	req.Header.Set("Content-Type", mimeType)
	req.Header.Set("Content-Length", strconv.FormatInt(size, 10))
	resp, err := client.httpClient.Do(req)
	if err != nil {
		return CreativeAssetObjectInfo{}, errors.New("s3-compatible put failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return CreativeAssetObjectInfo{}, fmt.Errorf("s3-compatible put failed with status %d", resp.StatusCode)
	}
	return CreativeAssetObjectInfo{
		ETag:    strings.Trim(resp.Header.Get("ETag"), `"`),
		Version: resp.Header.Get("x-amz-version-id"),
		Size:    size,
	}, nil
}

func (client *HTTPS3CompatibleObjectClient) GetObject(ctx context.Context, key string, start int64, end int64) (io.ReadCloser, CreativeAssetObjectInfo, error) {
	req, err := client.newSignedRequest(ctx, http.MethodGet, key, nil, 0, emptySHA256Hex)
	if err != nil {
		return nil, CreativeAssetObjectInfo{}, err
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))
	resp, err := client.httpClient.Do(req)
	if err != nil {
		return nil, CreativeAssetObjectInfo{}, errors.New("s3-compatible get failed")
	}
	if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusNotFound {
			return nil, CreativeAssetObjectInfo{}, ErrCreativeAssetNotFound
		}
		if resp.StatusCode == http.StatusRequestedRangeNotSatisfiable {
			return nil, CreativeAssetObjectInfo{}, ErrCreativeAssetRangeNotSatisfiable
		}
		return nil, CreativeAssetObjectInfo{}, fmt.Errorf("s3-compatible get failed with status %d", resp.StatusCode)
	}
	size := int64(-1)
	if parsed, parseErr := strconv.ParseInt(resp.Header.Get("Content-Length"), 10, 64); parseErr == nil {
		size = parsed
	}
	return resp.Body, CreativeAssetObjectInfo{
		ETag:    strings.Trim(resp.Header.Get("ETag"), `"`),
		Version: resp.Header.Get("x-amz-version-id"),
		Size:    size,
	}, nil
}

func (client *HTTPS3CompatibleObjectClient) HeadObject(ctx context.Context, key string) (CreativeAssetObjectInfo, error) {
	req, err := client.newSignedRequest(ctx, http.MethodHead, key, nil, 0, emptySHA256Hex)
	if err != nil {
		return CreativeAssetObjectInfo{}, err
	}
	resp, err := client.httpClient.Do(req)
	if err != nil {
		return CreativeAssetObjectInfo{}, errors.New("s3-compatible head failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return CreativeAssetObjectInfo{}, ErrCreativeAssetNotFound
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return CreativeAssetObjectInfo{}, fmt.Errorf("s3-compatible head failed with status %d", resp.StatusCode)
	}
	size, _ := strconv.ParseInt(resp.Header.Get("Content-Length"), 10, 64)
	return CreativeAssetObjectInfo{
		ETag:    strings.Trim(resp.Header.Get("ETag"), `"`),
		Version: resp.Header.Get("x-amz-version-id"),
		Size:    size,
	}, nil
}

func (client *HTTPS3CompatibleObjectClient) DeleteObject(ctx context.Context, key string) error {
	req, err := client.newSignedRequest(ctx, http.MethodDelete, key, nil, 0, emptySHA256Hex)
	if err != nil {
		return err
	}
	resp, err := client.httpClient.Do(req)
	if err != nil {
		return errors.New("s3-compatible delete failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("s3-compatible delete failed with status %d", resp.StatusCode)
	}
	return nil
}

const emptySHA256Hex = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

func (client *HTTPS3CompatibleObjectClient) newSignedRequest(ctx context.Context, method string, key string, body io.Reader, size int64, payloadHash string) (*http.Request, error) {
	objectURL, err := client.objectURL(key)
	if err != nil {
		return nil, err
	}
	if body == nil {
		body = http.NoBody
	}
	req, err := http.NewRequestWithContext(ctx, method, objectURL, body)
	if err != nil {
		return nil, errors.New("s3-compatible request build failed")
	}
	req.Header.Set("x-amz-content-sha256", payloadHash)
	if size > 0 {
		req.ContentLength = size
	}
	if err := client.signer.SignHTTP(ctx, client.credentials, req, payloadHash, "s3", client.cfg.S3Region, time.Now()); err != nil {
		return nil, errors.New("s3-compatible request signing failed")
	}
	return req, nil
}

func (client *HTTPS3CompatibleObjectClient) objectURL(key string) (string, error) {
	endpoint, err := url.Parse(client.cfg.S3Endpoint)
	if err != nil || endpoint.Scheme == "" || endpoint.Host == "" {
		return "", errors.New("s3-compatible endpoint is invalid")
	}
	escapedKey := escapeS3ObjectKey(key)
	basePath := strings.Trim(endpoint.EscapedPath(), "/")
	if client.cfg.S3ForcePathStyle {
		endpoint.Path = joinURLPath(basePath, client.cfg.S3Bucket, escapedKey)
	} else {
		endpoint.Host = client.cfg.S3Bucket + "." + endpoint.Host
		endpoint.Path = joinURLPath(basePath, escapedKey)
	}
	endpoint.RawQuery = ""
	endpoint.Fragment = ""
	return endpoint.String(), nil
}

func escapeS3ObjectKey(key string) string {
	parts := strings.Split(strings.TrimLeft(key, "/"), "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}

func joinURLPath(parts ...string) string {
	cleaned := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.Trim(part, "/")
		if part != "" {
			cleaned = append(cleaned, part)
		}
	}
	if len(cleaned) == 0 {
		return "/"
	}
	return "/" + strings.Join(cleaned, "/")
}

type fakeS3Object struct {
	data    []byte
	mime    string
	etag    string
	version string
}

func NewFakeS3CompatibleObjectClient() *FakeS3CompatibleObjectClient {
	return &FakeS3CompatibleObjectClient{objects: map[string]fakeS3Object{}}
}

func (client *FakeS3CompatibleObjectClient) PutObject(ctx context.Context, key string, body io.Reader, size int64, mimeType string) (CreativeAssetObjectInfo, error) {
	data, err := io.ReadAll(body)
	if err != nil {
		return CreativeAssetObjectInfo{}, err
	}
	if int64(len(data)) != size {
		return CreativeAssetObjectInfo{}, errors.New("s3 put size mismatch")
	}
	sum := sha256.Sum256(data)
	info := CreativeAssetObjectInfo{
		ETag:    hex.EncodeToString(sum[:]),
		Version: fmt.Sprintf("v%d", time.Now().UnixNano()),
		Size:    size,
	}
	client.mu.Lock()
	client.objects[key] = fakeS3Object{data: append([]byte(nil), data...), mime: mimeType, etag: info.ETag, version: info.Version}
	client.mu.Unlock()
	return info, nil
}

func (client *FakeS3CompatibleObjectClient) GetObject(ctx context.Context, key string, start int64, end int64) (io.ReadCloser, CreativeAssetObjectInfo, error) {
	client.mu.RLock()
	object, ok := client.objects[key]
	client.mu.RUnlock()
	if !ok {
		return nil, CreativeAssetObjectInfo{}, ErrCreativeAssetNotFound
	}
	if start < 0 || end < start || end >= int64(len(object.data)) {
		return nil, CreativeAssetObjectInfo{}, ErrCreativeAssetRangeNotSatisfiable
	}
	data := append([]byte(nil), object.data[start:end+1]...)
	return io.NopCloser(bytes.NewReader(data)), CreativeAssetObjectInfo{ETag: object.etag, Version: object.version, Size: int64(len(object.data))}, nil
}

func (client *FakeS3CompatibleObjectClient) HeadObject(ctx context.Context, key string) (CreativeAssetObjectInfo, error) {
	client.mu.RLock()
	object, ok := client.objects[key]
	client.mu.RUnlock()
	if !ok {
		return CreativeAssetObjectInfo{}, ErrCreativeAssetNotFound
	}
	return CreativeAssetObjectInfo{ETag: object.etag, Version: object.version, Size: int64(len(object.data))}, nil
}

func (client *FakeS3CompatibleObjectClient) DeleteObject(ctx context.Context, key string) error {
	client.mu.Lock()
	delete(client.objects, key)
	client.mu.Unlock()
	return nil
}

func normalizeCreativeAssetConfig(cfg CreativeAssetConfig) CreativeAssetConfig {
	cfg.RolloutMode = strings.ToLower(strings.TrimSpace(cfg.RolloutMode))
	if cfg.RolloutMode == "" {
		cfg.RolloutMode = CreativeAssetRolloutLocal
	}
	cfg.StorageBackend = strings.TrimSpace(cfg.StorageBackend)
	if cfg.StorageBackend == "" {
		cfg.StorageBackend = model.CreativeAssetStorageDatabase
	}
	if cfg.UserMaxBytes <= 0 {
		cfg.UserMaxBytes = CreativeAssetDefaultUserMax
	}
	if cfg.UserMaxAssets <= 0 {
		cfg.UserMaxAssets = CreativeAssetDefaultMaxCount
	}
	if cfg.DatabaseGlobalMaxBytes <= 0 {
		cfg.DatabaseGlobalMaxBytes = creativeAssetDefaultDBGlobalMaxBytes
	}
	if cfg.DatabaseUserMaxBytes <= 0 {
		cfg.DatabaseUserMaxBytes = creativeAssetDefaultDBUserMaxBytes
	}
	if cfg.DatabaseReservedFreeBytes < 0 {
		cfg.DatabaseReservedFreeBytes = 0
	}
	if cfg.DiskSpaceProviderForWrites == nil {
		cfg.DiskSpaceProviderForWrites = func() common.DiskSpaceInfo {
			return common.GetDiskSpaceInfo()
		}
	}
	cfg.S3Prefix = strings.Trim(strings.TrimSpace(cfg.S3Prefix), "/")
	cfg.S3Endpoint = strings.TrimSpace(cfg.S3Endpoint)
	cfg.S3Region = strings.TrimSpace(cfg.S3Region)
	cfg.S3Bucket = strings.TrimSpace(cfg.S3Bucket)
	cfg.S3AccessKeyID = strings.TrimSpace(cfg.S3AccessKeyID)
	cfg.S3SecretAccessKey = strings.TrimSpace(cfg.S3SecretAccessKey)
	if cfg.S3RequestTimeoutSeconds <= 0 {
		cfg.S3RequestTimeoutSeconds = creativeAssetDefaultS3RequestTimeoutSec
	}
	return cfg
}

func creativeAssetRolloutModeAllowed(mode string) bool {
	switch mode {
	case CreativeAssetRolloutLocal, CreativeAssetRolloutCanary, CreativeAssetRolloutProduction:
		return true
	default:
		return false
	}
}

func creativeS3ConfigComplete(cfg CreativeAssetConfig) bool {
	return cfg.S3Endpoint != "" && cfg.S3Region != "" && cfg.S3Bucket != "" && cfg.S3AccessKeyID != "" && cfg.S3SecretAccessKey != ""
}

func creativeS3EndpointHTTPS(endpoint string) bool {
	parsed, err := url.Parse(strings.TrimSpace(endpoint))
	return err == nil && parsed.Scheme == "https" && parsed.Host != ""
}

func readCreativeAssetBytes(reader io.Reader, maxBytes int64) ([]byte, error) {
	var buffer bytes.Buffer
	n, err := io.Copy(&buffer, io.LimitReader(reader, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if n > maxBytes {
		return nil, ErrCreativeAssetTooLarge
	}
	if n == 0 {
		return nil, fmt.Errorf("%w: file is empty", ErrCreativeAssetInvalid)
	}
	return buffer.Bytes(), nil
}

func generateCreativeAssetId() (string, error) {
	random, err := common.GenerateRandomCharsKey(24)
	if err != nil {
		return "", err
	}
	return "asset_" + random, nil
}

func detectCreativeAssetType(data []byte, clientMime string, clientMediaType string) (string, string, error) {
	clientMime = normalizeCreativeMime(clientMime)
	clientMediaType = strings.ToLower(strings.TrimSpace(clientMediaType))
	if clientMediaType != "" && clientMediaType != "image" && clientMediaType != "audio" && clientMediaType != "video" {
		return "", "", fmt.Errorf("%w: unsupported media type", ErrCreativeAssetInvalid)
	}
	if looksLikeSVG(data) || clientMime == "image/svg+xml" {
		return "", "", fmt.Errorf("%w: svg is not supported", ErrCreativeAssetInvalid)
	}
	if clientMime != "" && !creativeMimeAllowed(clientMime) {
		return "", "", fmt.Errorf("%w: unsupported MIME type", ErrCreativeAssetInvalid)
	}
	sniffed := normalizeCreativeMime(sniffCreativeMime(data))
	finalMime := sniffed
	if finalMime == "" || finalMime == "application/octet-stream" {
		finalMime = clientMime
	}
	if finalMime == "" || !creativeMimeAllowed(finalMime) {
		return "", "", fmt.Errorf("%w: unsupported MIME type", ErrCreativeAssetInvalid)
	}
	if clientMime != "" && sniffed != "" && sniffed != "application/octet-stream" && clientMime != sniffed {
		return "", "", fmt.Errorf("%w: MIME type mismatch", ErrCreativeAssetInvalid)
	}
	mediaType := strings.Split(finalMime, "/")[0]
	if finalMime == "audio/mp3" {
		mediaType = "audio"
	}
	if clientMediaType != "" && clientMediaType != mediaType {
		return "", "", fmt.Errorf("%w: media type mismatch", ErrCreativeAssetInvalid)
	}
	return mediaType, finalMime, nil
}

func normalizeCreativeMime(mime string) string {
	mime = strings.ToLower(strings.TrimSpace(strings.Split(mime, ";")[0]))
	if mime == "audio/x-wav" {
		return "audio/wav"
	}
	if mime == "audio/mpg" {
		return "audio/mpeg"
	}
	return mime
}

func sniffCreativeMime(data []byte) string {
	if len(data) >= 12 && bytes.Equal(data[0:4], []byte{'R', 'I', 'F', 'F'}) && bytes.Equal(data[8:12], []byte{'W', 'E', 'B', 'P'}) {
		return "image/webp"
	}
	if len(data) >= 12 && bytes.Equal(data[4:8], []byte{'f', 't', 'y', 'p'}) {
		return "video/mp4"
	}
	if len(data) >= 4 && bytes.Equal(data[0:4], []byte{'O', 'g', 'g', 'S'}) {
		return "audio/ogg"
	}
	if len(data) >= 4 && bytes.Equal(data[0:4], []byte{'R', 'I', 'F', 'F'}) {
		return "audio/wav"
	}
	if len(data) >= 4 && bytes.Equal(data[0:4], []byte{0x1A, 0x45, 0xDF, 0xA3}) {
		return "video/webm"
	}
	if len(data) >= 3 && bytes.Equal(data[0:3], []byte{'I', 'D', '3'}) {
		return "audio/mpeg"
	}
	if len(data) >= 2 && data[0] == 0xff && (data[1]&0xe0) == 0xe0 {
		return "audio/mpeg"
	}
	return http.DetectContentType(data)
}

func creativeMimeAllowed(mime string) bool {
	switch mime {
	case "image/png", "image/jpeg", "image/webp", "image/gif",
		"audio/mpeg", "audio/mp3", "audio/wav", "audio/ogg", "audio/webm", "audio/mp4", "audio/aac",
		"video/mp4", "video/webm", "video/quicktime", "video/x-m4v":
		return true
	default:
		return false
	}
}

func looksLikeSVG(data []byte) bool {
	trimmed := strings.TrimSpace(strings.ToLower(string(data[:creativeAssetMinInt(len(data), 512)])))
	return strings.HasPrefix(trimmed, "<svg") || strings.Contains(trimmed, "<svg ")
}

func parseCreativeHTTPRange(rangeHeader string, size int64) (int64, int64, int, string, error) {
	if size <= 0 {
		return 0, 0, http.StatusOK, "", ErrCreativeAssetNotFound
	}
	rangeHeader = strings.TrimSpace(rangeHeader)
	if rangeHeader == "" {
		return 0, size - 1, http.StatusOK, "", nil
	}
	if !strings.HasPrefix(rangeHeader, "bytes=") || strings.Contains(rangeHeader, ",") {
		return 0, 0, http.StatusRequestedRangeNotSatisfiable, "", ErrCreativeAssetRangeNotSatisfiable
	}
	spec := strings.TrimSpace(strings.TrimPrefix(rangeHeader, "bytes="))
	dash := strings.IndexByte(spec, '-')
	if dash < 0 {
		return 0, 0, http.StatusRequestedRangeNotSatisfiable, "", ErrCreativeAssetRangeNotSatisfiable
	}
	startText := strings.TrimSpace(spec[:dash])
	endText := strings.TrimSpace(spec[dash+1:])
	var start, end int64
	if startText == "" {
		suffix, err := strconv.ParseInt(endText, 10, 64)
		if err != nil || suffix <= 0 {
			return 0, 0, http.StatusRequestedRangeNotSatisfiable, "", ErrCreativeAssetRangeNotSatisfiable
		}
		if suffix > size {
			suffix = size
		}
		start = size - suffix
		end = size - 1
	} else {
		parsedStart, err := strconv.ParseInt(startText, 10, 64)
		if err != nil || parsedStart < 0 {
			return 0, 0, http.StatusRequestedRangeNotSatisfiable, "", ErrCreativeAssetRangeNotSatisfiable
		}
		start = parsedStart
		if endText == "" {
			end = size - 1
		} else {
			parsedEnd, err := strconv.ParseInt(endText, 10, 64)
			if err != nil || parsedEnd < start {
				return 0, 0, http.StatusRequestedRangeNotSatisfiable, "", ErrCreativeAssetRangeNotSatisfiable
			}
			end = parsedEnd
		}
	}
	if start >= size {
		return 0, 0, http.StatusRequestedRangeNotSatisfiable, "", ErrCreativeAssetRangeNotSatisfiable
	}
	if end >= size {
		end = size - 1
	}
	contentRange := fmt.Sprintf("bytes %d-%d/%d", start, end, size)
	return start, end, http.StatusPartialContent, contentRange, nil
}

func rejectCreativeAssetMetadata(metadata map[string]string) error {
	for key, value := range metadata {
		normalized := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(key), "_", ""), "-", ""))
		if normalized == "sourceurl" || normalized == "objectkey" || normalized == "bucketurl" || normalized == "signedurl" {
			return fmt.Errorf("%w: forbidden asset metadata field", ErrCreativeAssetInvalid)
		}
		if creativeAssetStringLooksCredentialed(value) {
			return fmt.Errorf("%w: forbidden asset metadata value", ErrCreativeAssetInvalid)
		}
	}
	return nil
}

func creativeAssetStringLooksCredentialed(value string) bool {
	lower := strings.ToLower(value)
	if !strings.Contains(lower, "http://") && !strings.Contains(lower, "https://") {
		return false
	}
	for _, marker := range []string{"x-amz-signature", "x-amz-credential", "awsaccesskeyid", "signature=", "token=", "access_token=", "api_key=", "apikey=", "secret=", "expires="} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func scrubCreativeAssetError(err error) error {
	if err == nil {
		return nil
	}
	text := err.Error()
	for _, sensitive := range []string{"CREATIVE_ASSET_S3_ACCESS_KEY_ID", "CREATIVE_ASSET_S3_SECRET_ACCESS_KEY"} {
		text = strings.ReplaceAll(text, sensitive, "[redacted]")
	}
	if strings.Contains(strings.ToLower(text), "secret") && len(text) > 256 {
		text = text[:256]
	}
	return errors.New(text)
}

func envInt64(name string, defaultValue int64) int64 {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return defaultValue
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return defaultValue
	}
	return value
}

func creativeAssetMinInt(a int, b int) int {
	if a < b {
		return a
	}
	return b
}

func isUniqueConstraintError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "unique") || strings.Contains(lower, "duplicate")
}
