package model

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	CreativeAssetStorageDatabase     = "database"
	CreativeAssetStorageS3Compatible = "s3-compatible"
	CreativeAssetStatusActive        = "active"
	CreativeAssetStatusPendingDelete = "pending_delete"
	CreativeDocumentAssetRefLimit    = 1000

	CreativeAssetLifecycleOperationUploadCleanup = "upload_cleanup"
	CreativeAssetLifecycleStatusPending          = "pending"
	CreativeAssetLifecycleStatusProcessing       = "processing"
	CreativeAssetLifecycleStatusDone             = "done"
	CreativeAssetLifecycleStatusFailed           = "failed"

	creativeAssetQuotaGlobalUserId = 0
)

var creativeAssetIdPattern = regexp.MustCompile(`^asset_[A-Za-z0-9_-]{6,122}$`)

var (
	ErrCreativeAssetQuotaExceeded = errors.New("creative_asset_quota_exceeded")
	ErrCreativeAssetReferenced    = errors.New("creative asset is referenced")
)

// CreativeAsset is the owner-scoped binary metadata authority for embedded
// /creative. StorageBackend/ObjectKey are internal storage details and must not
// be serialized to Opentu or document snapshots.
type CreativeAsset struct {
	Id               int    `json:"-" gorm:"primaryKey"`
	UserId           int    `json:"user_id" gorm:"not null;index;uniqueIndex:idx_creative_asset_user_asset;uniqueIndex:idx_creative_asset_user_hash"`
	AssetId          string `json:"asset_id" gorm:"column:asset_id;type:varchar(128);not null;uniqueIndex:idx_creative_asset_user_asset"`
	ContentHash      string `json:"content_hash" gorm:"type:varchar(64);not null;uniqueIndex:idx_creative_asset_user_hash;index"`
	MediaType        string `json:"media_type" gorm:"type:varchar(16);not null"`
	MimeType         string `json:"mime_type" gorm:"type:varchar(128);not null"`
	SizeBytes        int64  `json:"size_bytes" gorm:"bigint;not null;index"`
	StorageBackend   string `json:"-" gorm:"type:varchar(32);not null;index"`
	ObjectKey        string `json:"-" gorm:"type:varchar(512)"`
	ObjectETag       string `json:"-" gorm:"type:varchar(255)"`
	ObjectVersion    string `json:"-" gorm:"type:varchar(255)"`
	Data             []byte `json:"-"`
	Status           string `json:"-" gorm:"type:varchar(32);not null;default:'active';index"`
	DeletingTime     int64  `json:"-" gorm:"bigint;index"`
	DeleteError      string `json:"-" gorm:"type:text"`
	CreatedTime      int64  `json:"created_time" gorm:"bigint;index"`
	UpdatedTime      int64  `json:"updated_time" gorm:"bigint;index"`
	LastAccessedTime int64  `json:"last_accessed_time" gorm:"bigint;index"`
}

// CreativeAssetQuota is the per-owner reservation row used to serialize quota
// checks with asset metadata writes across API workers.
type CreativeAssetQuota struct {
	UserId            int   `json:"user_id" gorm:"primaryKey;autoIncrement:false"`
	AssetCount        int64 `json:"asset_count" gorm:"bigint;not null;default:0"`
	TotalSizeBytes    int64 `json:"total_size_bytes" gorm:"bigint;not null;default:0"`
	DatabaseSizeBytes int64 `json:"database_size_bytes" gorm:"bigint;not null;default:0"`
	UpdatedTime       int64 `json:"updated_time" gorm:"bigint;index"`
}

type CreativeAssetQuotaLimits struct {
	MaxAssets              int64
	MaxBytes               int64
	DatabaseUserMaxBytes   int64
	DatabaseGlobalMaxBytes int64
	EnforceDatabaseLimits  bool
}

// CreativeAssetLifecycleOutbox records storage objects that need durable
// cleanup after metadata write/dedupe failures. It intentionally stores only
// internal object identifiers; public API DTOs must never serialize this model.
type CreativeAssetLifecycleOutbox struct {
	ID             int64  `json:"-" gorm:"primaryKey"`
	UserId         int    `json:"-" gorm:"not null;index;uniqueIndex:idx_creative_asset_lifecycle_work,priority:1"`
	AssetId        string `json:"-" gorm:"type:varchar(128);not null;index;uniqueIndex:idx_creative_asset_lifecycle_work,priority:2"`
	ObjectKey      string `json:"-" gorm:"type:varchar(512);not null;uniqueIndex:idx_creative_asset_lifecycle_work,priority:3"`
	StorageBackend string `json:"-" gorm:"type:varchar(32);not null;index"`
	Operation      string `json:"-" gorm:"type:varchar(40);not null;index;uniqueIndex:idx_creative_asset_lifecycle_work,priority:4"`
	Status         string `json:"-" gorm:"type:varchar(20);not null;index"`
	Attempts       int    `json:"-"`
	LastError      string `json:"-" gorm:"type:text"`
	CreatedTime    int64  `json:"-" gorm:"bigint;index"`
	UpdatedTime    int64  `json:"-" gorm:"bigint;index"`
}

func (asset *CreativeAsset) BeforeSave(tx *gorm.DB) error {
	asset.AssetId = strings.TrimSpace(asset.AssetId)
	asset.ContentHash = strings.TrimSpace(asset.ContentHash)
	asset.MediaType = strings.TrimSpace(asset.MediaType)
	asset.MimeType = strings.TrimSpace(asset.MimeType)
	asset.StorageBackend = strings.TrimSpace(asset.StorageBackend)
	asset.Status = strings.TrimSpace(asset.Status)
	if asset.Status == "" {
		asset.Status = CreativeAssetStatusActive
	}
	if asset.StorageBackend == CreativeAssetStorageDatabase {
		asset.ObjectKey = ""
		asset.ObjectETag = ""
		asset.ObjectVersion = ""
	} else if asset.StorageBackend == CreativeAssetStorageS3Compatible {
		asset.Data = nil
	} else {
		return fmt.Errorf("unsupported creative asset storage backend: %s", asset.StorageBackend)
	}
	now := time.Now().Unix()
	if asset.CreatedTime == 0 {
		asset.CreatedTime = now
	}
	if asset.UpdatedTime == 0 {
		asset.UpdatedTime = now
	}
	if asset.LastAccessedTime == 0 {
		asset.LastAccessedTime = now
	}
	return nil
}

func (outbox *CreativeAssetLifecycleOutbox) BeforeSave(tx *gorm.DB) error {
	outbox.AssetId = strings.TrimSpace(outbox.AssetId)
	outbox.ObjectKey = strings.TrimSpace(outbox.ObjectKey)
	outbox.StorageBackend = strings.TrimSpace(outbox.StorageBackend)
	outbox.Operation = strings.TrimSpace(outbox.Operation)
	outbox.Status = strings.TrimSpace(outbox.Status)
	if outbox.Status == "" {
		outbox.Status = CreativeAssetLifecycleStatusPending
	}
	now := time.Now().Unix()
	if outbox.CreatedTime == 0 {
		outbox.CreatedTime = now
	}
	if outbox.UpdatedTime == 0 {
		outbox.UpdatedTime = now
	}
	return nil
}

// CreativeDocumentAssetRef links a creative document snapshot to the cloud
// asset ids it references. The link is owner-scoped so GC/delete never crosses
// users.
type CreativeDocumentAssetRef struct {
	UserId      int    `json:"user_id" gorm:"primaryKey;autoIncrement:false"`
	DocumentId  string `json:"document_id" gorm:"column:document_id;type:varchar(128);primaryKey;autoIncrement:false;index"`
	AssetId     string `json:"asset_id" gorm:"column:asset_id;type:varchar(128);primaryKey;autoIncrement:false;index"`
	CreatedTime int64  `json:"created_time" gorm:"bigint;index"`
}

func IsValidCreativeAssetId(assetId string) bool {
	assetId = strings.TrimSpace(assetId)
	return creativeAssetIdPattern.MatchString(assetId)
}

func GetCreativeAsset(userId int, assetId string) (*CreativeAsset, bool, error) {
	var asset CreativeAsset
	err := activeCreativeAssetQuery(DB).Where("user_id = ? AND asset_id = ?", userId, strings.TrimSpace(assetId)).First(&asset).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return &asset, true, nil
}

func GetCreativeAssetByContentHash(userId int, contentHash string) (*CreativeAsset, bool, error) {
	var asset CreativeAsset
	err := activeCreativeAssetQuery(DB).Where("user_id = ? AND content_hash = ?", userId, strings.TrimSpace(contentHash)).First(&asset).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return &asset, true, nil
}

func CountCreativeAssets(userId int) (int64, error) {
	var count int64
	err := DB.Model(&CreativeAsset{}).Where("user_id = ?", userId).Count(&count).Error
	return count, err
}

func SumCreativeAssetBytes(userId int) (int64, error) {
	var total int64
	err := DB.Model(&CreativeAsset{}).Where("user_id = ?", userId).Select("COALESCE(SUM(size_bytes), 0)").Scan(&total).Error
	return total, err
}

func SumCreativeDatabaseAssetBytes(userId *int) (int64, error) {
	query := DB.Model(&CreativeAsset{}).Where("storage_backend = ?", CreativeAssetStorageDatabase)
	if userId != nil {
		query = query.Where("user_id = ?", *userId)
	}
	var total int64
	err := query.Select("COALESCE(SUM(size_bytes), 0)").Scan(&total).Error
	return total, err
}

func TouchCreativeAssetAccessedTime(userId int, assetId string) error {
	return activeCreativeAssetQuery(DB.Model(&CreativeAsset{})).
		Where("user_id = ? AND asset_id = ?", userId, assetId).
		UpdateColumn("last_accessed_time", time.Now().Unix()).Error
}

func CountCreativeAssetRefs(userId int, assetId string) (int64, error) {
	var count int64
	err := DB.Model(&CreativeDocumentAssetRef{}).Where("user_id = ? AND asset_id = ?", userId, assetId).Count(&count).Error
	return count, err
}

func CreateCreativeAssetWithQuota(asset *CreativeAsset, limits CreativeAssetQuotaLimits) (*CreativeAsset, bool, error) {
	if asset == nil {
		return nil, false, errors.New("creative asset is nil")
	}
	asset.Status = strings.TrimSpace(asset.Status)
	if asset.Status == "" {
		asset.Status = CreativeAssetStatusActive
	}
	var result *CreativeAsset
	duplicate := false
	err := DB.Transaction(func(tx *gorm.DB) error {
		if existing, exists, err := getCreativeAssetByContentHashTx(tx, asset.UserId, asset.ContentHash); err != nil || exists {
			if exists {
				result = existing
				duplicate = true
			}
			return err
		}

		userQuota, err := lockCreativeAssetQuotaTx(tx, asset.UserId)
		if err != nil {
			return err
		}
		if creativeAssetQuotaWouldExceed(userQuota.AssetCount, asset.SizeBytes, userQuota.TotalSizeBytes, limits.MaxAssets, limits.MaxBytes) {
			return ErrCreativeAssetQuotaExceeded
		}
		if limits.EnforceDatabaseLimits && asset.StorageBackend == CreativeAssetStorageDatabase {
			if limits.DatabaseUserMaxBytes > 0 && userQuota.DatabaseSizeBytes+asset.SizeBytes > limits.DatabaseUserMaxBytes {
				return ErrCreativeAssetQuotaExceeded
			}
			globalQuota, err := lockCreativeAssetQuotaTx(tx, creativeAssetQuotaGlobalUserId)
			if err != nil {
				return err
			}
			if limits.DatabaseGlobalMaxBytes > 0 && globalQuota.DatabaseSizeBytes+asset.SizeBytes > limits.DatabaseGlobalMaxBytes {
				return ErrCreativeAssetQuotaExceeded
			}
			incrementCreativeAssetQuota(globalQuota, asset)
			if err := saveCreativeAssetQuotaTx(tx, globalQuota); err != nil {
				return err
			}
		}

		if err := tx.Create(asset).Error; err != nil {
			return err
		}
		incrementCreativeAssetQuota(userQuota, asset)
		if err := saveCreativeAssetQuotaTx(tx, userQuota); err != nil {
			return err
		}
		result = asset
		return nil
	})
	return result, duplicate, err
}

func MarkCreativeAssetPendingDelete(userId int, assetId string) (*CreativeAsset, bool, error) {
	assetId = strings.TrimSpace(assetId)
	if !IsValidCreativeAssetId(assetId) {
		return nil, false, nil
	}
	var marked *CreativeAsset
	found := false
	err := DB.Transaction(func(tx *gorm.DB) error {
		var asset CreativeAsset
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("user_id = ? AND asset_id = ? AND (status = ? OR status = ? OR status = '')", userId, assetId, CreativeAssetStatusActive, CreativeAssetStatusPendingDelete).
			First(&asset).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		var refCount int64
		if err := tx.Model(&CreativeDocumentAssetRef{}).Where("user_id = ? AND asset_id = ?", userId, assetId).Count(&refCount).Error; err != nil {
			return err
		}
		if refCount > 0 {
			return ErrCreativeAssetReferenced
		}
		found = true
		now := time.Now().Unix()
		if asset.Status != CreativeAssetStatusPendingDelete {
			if err := tx.Model(&CreativeAsset{}).
				Where("user_id = ? AND asset_id = ?", userId, assetId).
				UpdateColumns(map[string]any{
					"status":        CreativeAssetStatusPendingDelete,
					"deleting_time": now,
					"delete_error":  "",
					"updated_time":  now,
				}).Error; err != nil {
				return err
			}
			asset.Status = CreativeAssetStatusPendingDelete
			asset.DeletingTime = now
			asset.DeleteError = ""
			asset.UpdatedTime = now
		}
		marked = &asset
		return nil
	})
	return marked, found, err
}

func MarkCreativeAssetDeleteFailed(userId int, assetId string, deleteErr error) error {
	if deleteErr == nil {
		return nil
	}
	text := deleteErr.Error()
	if len(text) > 1024 {
		text = text[:1024]
	}
	return DB.Model(&CreativeAsset{}).
		Where("user_id = ? AND asset_id = ? AND status = ?", userId, strings.TrimSpace(assetId), CreativeAssetStatusPendingDelete).
		UpdateColumns(map[string]any{
			"delete_error": text,
			"updated_time": time.Now().Unix(),
		}).Error
}

func ConfirmCreativeAssetPendingDeleteForStorage(userId int, assetId string) (*CreativeAsset, bool, error) {
	assetId = strings.TrimSpace(assetId)
	if !IsValidCreativeAssetId(assetId) {
		return nil, false, nil
	}
	var confirmed *CreativeAsset
	found := false
	err := DB.Transaction(func(tx *gorm.DB) error {
		var asset CreativeAsset
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("user_id = ? AND asset_id = ? AND status = ?", userId, assetId, CreativeAssetStatusPendingDelete).
			First(&asset).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		var refCount int64
		if err := tx.Model(&CreativeDocumentAssetRef{}).Where("user_id = ? AND asset_id = ?", userId, assetId).Count(&refCount).Error; err != nil {
			return err
		}
		if refCount > 0 {
			return ErrCreativeAssetReferenced
		}
		found = true
		confirmed = &asset
		return nil
	})
	return confirmed, found, err
}

func FinalizeCreativeAssetDelete(userId int, assetId string) error {
	assetId = strings.TrimSpace(assetId)
	if !IsValidCreativeAssetId(assetId) {
		return nil
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		var asset CreativeAsset
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("user_id = ? AND asset_id = ? AND (status = ? OR status = ? OR status = '')", userId, assetId, CreativeAssetStatusActive, CreativeAssetStatusPendingDelete).
			First(&asset).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		var refCount int64
		if err := tx.Model(&CreativeDocumentAssetRef{}).Where("user_id = ? AND asset_id = ?", userId, assetId).Count(&refCount).Error; err != nil {
			return err
		}
		if refCount > 0 {
			return ErrCreativeAssetReferenced
		}
		userQuota, err := lockCreativeAssetQuotaTx(tx, userId)
		if err != nil {
			return err
		}
		var globalQuota *CreativeAssetQuota
		if asset.StorageBackend == CreativeAssetStorageDatabase {
			globalQuota, err = lockCreativeAssetQuotaTx(tx, creativeAssetQuotaGlobalUserId)
			if err != nil {
				return err
			}
		}
		if err := tx.Delete(&asset).Error; err != nil {
			return err
		}
		decrementCreativeAssetQuota(userQuota, &asset)
		if err := saveCreativeAssetQuotaTx(tx, userQuota); err != nil {
			return err
		}
		if globalQuota != nil {
			decrementCreativeAssetQuota(globalQuota, &asset)
			if err := saveCreativeAssetQuotaTx(tx, globalQuota); err != nil {
				return err
			}
		}
		return nil
	})
}

func EnqueueCreativeAssetLifecycleOutbox(asset *CreativeAsset, operation string) (*CreativeAssetLifecycleOutbox, error) {
	if asset == nil {
		return nil, errors.New("creative asset is nil")
	}
	operation = strings.TrimSpace(operation)
	if operation != CreativeAssetLifecycleOperationUploadCleanup {
		return nil, fmt.Errorf("unsupported creative asset lifecycle operation: %s", operation)
	}
	if asset.StorageBackend != CreativeAssetStorageS3Compatible {
		return nil, nil
	}
	if strings.TrimSpace(asset.ObjectKey) == "" {
		return nil, errors.New("creative asset object key is missing")
	}
	now := time.Now().Unix()
	outbox := &CreativeAssetLifecycleOutbox{
		UserId:         asset.UserId,
		AssetId:        strings.TrimSpace(asset.AssetId),
		ObjectKey:      strings.TrimSpace(asset.ObjectKey),
		StorageBackend: asset.StorageBackend,
		Operation:      operation,
		Status:         CreativeAssetLifecycleStatusPending,
		CreatedTime:    now,
		UpdatedTime:    now,
	}
	err := DB.Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: "user_id"},
			{Name: "asset_id"},
			{Name: "object_key"},
			{Name: "operation"},
		},
		DoUpdates: clause.Assignments(map[string]any{
			"status":       CreativeAssetLifecycleStatusPending,
			"attempts":     0,
			"last_error":   "",
			"updated_time": now,
		}),
	}).Create(outbox).Error
	return outbox, err
}

func ListPendingCreativeAssetLifecycleOutboxes(limit int) ([]CreativeAssetLifecycleOutbox, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	staleProcessingCutoff := time.Now().Unix() - 5*60
	var outboxes []CreativeAssetLifecycleOutbox
	err := DB.Where("status IN ? OR (status = ? AND updated_time < ?)",
		[]string{CreativeAssetLifecycleStatusPending, CreativeAssetLifecycleStatusFailed},
		CreativeAssetLifecycleStatusProcessing,
		staleProcessingCutoff,
	).Order("updated_time ASC, id ASC").Limit(limit).Find(&outboxes).Error
	return outboxes, err
}

func ClaimCreativeAssetLifecycleOutbox(id int64) (bool, error) {
	if id <= 0 {
		return false, nil
	}
	now := time.Now().Unix()
	staleProcessingCutoff := now - 5*60
	res := DB.Model(&CreativeAssetLifecycleOutbox{}).
		Where("id = ? AND (status IN ? OR (status = ? AND updated_time < ?))",
			id,
			[]string{CreativeAssetLifecycleStatusPending, CreativeAssetLifecycleStatusFailed},
			CreativeAssetLifecycleStatusProcessing,
			staleProcessingCutoff,
		).
		UpdateColumns(map[string]any{
			"status":       CreativeAssetLifecycleStatusProcessing,
			"attempts":     gorm.Expr("attempts + ?", 1),
			"updated_time": now,
		})
	return res.RowsAffected > 0, res.Error
}

func MarkCreativeAssetLifecycleOutboxDone(id int64) error {
	if id <= 0 {
		return nil
	}
	return DB.Model(&CreativeAssetLifecycleOutbox{}).
		Where("id = ?", id).
		UpdateColumns(map[string]any{
			"status":       CreativeAssetLifecycleStatusDone,
			"last_error":   "",
			"updated_time": time.Now().Unix(),
		}).Error
}

func MarkCreativeAssetLifecycleOutboxFailed(id int64, lifecycleErr error) error {
	if id <= 0 || lifecycleErr == nil {
		return nil
	}
	text := lifecycleErr.Error()
	if len(text) > 1024 {
		text = text[:1024]
	}
	return DB.Model(&CreativeAssetLifecycleOutbox{}).
		Where("id = ?", id).
		UpdateColumns(map[string]any{
			"status":       CreativeAssetLifecycleStatusFailed,
			"last_error":   text,
			"updated_time": time.Now().Unix(),
		}).Error
}

func ListPendingDeleteCreativeAssets(limit int) ([]CreativeAsset, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var assets []CreativeAsset
	err := DB.Where("status = ?", CreativeAssetStatusPendingDelete).
		Order("deleting_time ASC, id ASC").
		Limit(limit).
		Find(&assets).Error
	return assets, err
}

func RefreshCreativeDocumentAssetRefs(userId int, documentId string, assetIds []string) error {
	return DB.Transaction(func(tx *gorm.DB) error {
		return refreshCreativeDocumentAssetRefsTx(tx, userId, documentId, assetIds)
	})
}

func DeleteCreativeDocumentAssetRefs(userId int, documentId string) error {
	return DB.Transaction(func(tx *gorm.DB) error {
		return deleteCreativeDocumentAssetRefsTx(tx, userId, documentId)
	})
}

func ListCreativeDocumentAssetRefs(userId int, documentId string) ([]CreativeDocumentAssetRef, error) {
	var refs []CreativeDocumentAssetRef
	err := DB.Where("user_id = ? AND document_id = ?", userId, strings.TrimSpace(documentId)).
		Order("asset_id ASC").Find(&refs).Error
	return refs, err
}

func refreshCreativeDocumentAssetRefsTx(tx *gorm.DB, userId int, documentId string, assetIds []string) error {
	documentId = strings.TrimSpace(documentId)
	if documentId == "" {
		return errors.New("creative document id is required")
	}
	unique := make(map[string]struct{}, len(assetIds))
	for _, assetId := range assetIds {
		assetId = strings.TrimSpace(assetId)
		if assetId == "" {
			continue
		}
		if !IsValidCreativeAssetId(assetId) {
			return fmt.Errorf("creative asset reference is invalid")
		}
		unique[assetId] = struct{}{}
	}
	if len(unique) > CreativeDocumentAssetRefLimit {
		return fmt.Errorf("creative document has too many asset references")
	}

	now := time.Now().Unix()
	refs := make([]CreativeDocumentAssetRef, 0, len(unique))
	for assetId := range unique {
		refs = append(refs, CreativeDocumentAssetRef{
			UserId:      userId,
			DocumentId:  documentId,
			AssetId:     assetId,
			CreatedTime: now,
		})
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].AssetId < refs[j].AssetId })

	if err := validateCreativeDocumentAssetIdsExistTx(tx, userId, unique); err != nil {
		return err
	}
	if err := tx.Where("user_id = ? AND document_id = ?", userId, documentId).Delete(&CreativeDocumentAssetRef{}).Error; err != nil {
		return err
	}
	if len(refs) == 0 {
		return nil
	}
	return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&refs).Error
}

func deleteCreativeDocumentAssetRefsTx(tx *gorm.DB, userId int, documentId string) error {
	documentId = strings.TrimSpace(documentId)
	if documentId == "" {
		return errors.New("creative document id is required")
	}
	return tx.Where("user_id = ? AND document_id = ?", userId, documentId).Delete(&CreativeDocumentAssetRef{}).Error
}

func ValidateCreativeDocumentAssetRefs(userId int, snapshotJSON string, metadataJSON string, expectedOrigin string) ([]string, error) {
	assetIds := map[string]struct{}{}
	for _, jsonText := range []string{snapshotJSON, metadataJSON} {
		if strings.TrimSpace(jsonText) == "" {
			continue
		}
		var decoded any
		if err := common.Unmarshal([]byte(jsonText), &decoded); err != nil {
			return nil, err
		}
		if err := extractCreativeAssetIdsFromValue(decoded, expectedOrigin, assetIds); err != nil {
			return nil, err
		}
	}
	if len(assetIds) > CreativeDocumentAssetRefLimit {
		return nil, fmt.Errorf("creative document has too many asset references")
	}
	ids := make([]string, 0, len(assetIds))
	for assetId := range assetIds {
		if !IsValidCreativeAssetId(assetId) {
			return nil, fmt.Errorf("creative asset reference is invalid")
		}
		exists, err := creativeAssetExistsForUser(userId, assetId)
		if err != nil {
			return nil, err
		}
		if !exists {
			return nil, fmt.Errorf("creative asset reference is invalid")
		}
		ids = append(ids, assetId)
	}
	sort.Strings(ids)
	return ids, nil
}

func extractCreativeAssetIdsFromValue(value any, expectedOrigin string, assetIds map[string]struct{}) error {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			normalizedKey := strings.ToLower(strings.TrimSpace(key))
			if normalizedKey == "creativeassetid" || normalizedKey == "cloudassetid" {
				if assetId, ok := child.(string); ok && strings.TrimSpace(assetId) != "" {
					if !IsValidCreativeAssetId(assetId) {
						return fmt.Errorf("creative asset reference is invalid")
					}
					assetIds[strings.TrimSpace(assetId)] = struct{}{}
					continue
				}
			}
			if err := extractCreativeAssetIdsFromValue(child, expectedOrigin, assetIds); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range typed {
			if err := extractCreativeAssetIdsFromValue(child, expectedOrigin, assetIds); err != nil {
				return err
			}
		}
	case string:
		assetId, found, err := ParseCreativeAssetContentURL(typed, expectedOrigin)
		if err != nil {
			return err
		}
		if found {
			assetIds[assetId] = struct{}{}
		}
	}
	return nil
}

func ParseCreativeAssetContentURL(raw string, expectedOrigin string) (string, bool, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || !strings.Contains(raw, "/creative/api/assets/") {
		return "", false, nil
	}
	if strings.HasPrefix(raw, "//") {
		return "", true, fmt.Errorf("creative asset URL origin is invalid")
	}
	lowerRaw := strings.ToLower(raw)
	if strings.Contains(lowerRaw, "%2f") || strings.Contains(lowerRaw, "%5c") || strings.Contains(raw, "..") {
		return "", true, fmt.Errorf("creative asset URL path is invalid")
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return "", true, fmt.Errorf("creative asset URL is invalid")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", true, fmt.Errorf("creative asset URL must not include query or fragment")
	}
	if parsed.Scheme != "" || parsed.Host != "" {
		if parsed.Scheme != "http" && parsed.Scheme != "https" {
			return "", true, fmt.Errorf("creative asset URL origin is invalid")
		}
		expectedOrigin = strings.TrimRight(strings.TrimSpace(expectedOrigin), "/")
		actualOrigin := parsed.Scheme + "://" + parsed.Host
		if expectedOrigin == "" || actualOrigin != expectedOrigin {
			return "", true, fmt.Errorf("creative asset URL origin is invalid")
		}
	}

	path := parsed.EscapedPath()
	if path == "" {
		path = parsed.Path
	}
	if path != parsed.Path {
		return "", true, fmt.Errorf("creative asset URL path is invalid")
	}
	parts := strings.Split(parsed.Path, "/")
	if len(parts) != 6 ||
		parts[0] != "" ||
		parts[1] != "creative" ||
		parts[2] != "api" ||
		parts[3] != "assets" ||
		parts[5] != "content" {
		return "", true, fmt.Errorf("creative asset URL path is invalid")
	}
	assetId := parts[4]
	if !IsValidCreativeAssetId(assetId) {
		return "", true, fmt.Errorf("creative asset reference is invalid")
	}
	return assetId, true, nil
}

func creativeAssetExistsForUser(userId int, assetId string) (bool, error) {
	var count int64
	err := activeCreativeAssetQuery(DB.Model(&CreativeAsset{})).Where("user_id = ? AND asset_id = ?", userId, assetId).Count(&count).Error
	return count > 0, err
}

func getCreativeAssetByContentHashTx(tx *gorm.DB, userId int, contentHash string) (*CreativeAsset, bool, error) {
	var asset CreativeAsset
	err := activeCreativeAssetQuery(tx.Clauses(clause.Locking{Strength: "UPDATE"})).
		Where("user_id = ? AND content_hash = ?", userId, strings.TrimSpace(contentHash)).
		First(&asset).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return &asset, true, nil
}

func activeCreativeAssetQuery(db *gorm.DB) *gorm.DB {
	return db.Where("(status = ? OR status = '')", CreativeAssetStatusActive)
}

func creativeAssetQuotaWouldExceed(currentCount int64, incomingBytes int64, currentBytes int64, maxAssets int64, maxBytes int64) bool {
	if maxAssets > 0 && currentCount >= maxAssets {
		return true
	}
	return maxBytes > 0 && currentBytes+incomingBytes > maxBytes
}

func lockCreativeAssetQuotaTx(tx *gorm.DB, userId int) (*CreativeAssetQuota, error) {
	if err := ensureCreativeAssetQuotaRowTx(tx, userId); err != nil {
		return nil, err
	}
	var quota CreativeAssetQuota
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id = ?", userId).First(&quota).Error
	if err != nil {
		return nil, err
	}
	return &quota, nil
}

func ensureCreativeAssetQuotaRowTx(tx *gorm.DB, userId int) error {
	quota, err := creativeAssetQuotaSnapshotTx(tx, userId)
	if err != nil {
		return err
	}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(quota).Error; err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") || strings.Contains(strings.ToLower(err.Error()), "duplicate") {
			return nil
		}
		return err
	}
	return nil
}

func creativeAssetQuotaSnapshotTx(tx *gorm.DB, userId int) (*CreativeAssetQuota, error) {
	query := tx.Model(&CreativeAsset{})
	if userId == creativeAssetQuotaGlobalUserId {
		query = query.Where("storage_backend = ?", CreativeAssetStorageDatabase)
	} else {
		query = query.Where("user_id = ?", userId)
	}
	var assetCount int64
	if err := query.Count(&assetCount).Error; err != nil {
		return nil, err
	}
	totalQuery := tx.Model(&CreativeAsset{})
	if userId == creativeAssetQuotaGlobalUserId {
		totalQuery = totalQuery.Where("storage_backend = ?", CreativeAssetStorageDatabase)
	} else {
		totalQuery = totalQuery.Where("user_id = ?", userId)
	}
	var totalBytes int64
	if err := totalQuery.Select("COALESCE(SUM(size_bytes), 0)").Scan(&totalBytes).Error; err != nil {
		return nil, err
	}
	dbQuery := tx.Model(&CreativeAsset{}).Where("storage_backend = ?", CreativeAssetStorageDatabase)
	if userId != creativeAssetQuotaGlobalUserId {
		dbQuery = dbQuery.Where("user_id = ?", userId)
	}
	var dbBytes int64
	if err := dbQuery.Select("COALESCE(SUM(size_bytes), 0)").Scan(&dbBytes).Error; err != nil {
		return nil, err
	}
	return &CreativeAssetQuota{
		UserId:            userId,
		AssetCount:        assetCount,
		TotalSizeBytes:    totalBytes,
		DatabaseSizeBytes: dbBytes,
		UpdatedTime:       time.Now().Unix(),
	}, nil
}

func incrementCreativeAssetQuota(quota *CreativeAssetQuota, asset *CreativeAsset) {
	quota.AssetCount++
	quota.TotalSizeBytes += asset.SizeBytes
	if asset.StorageBackend == CreativeAssetStorageDatabase {
		quota.DatabaseSizeBytes += asset.SizeBytes
	}
	quota.UpdatedTime = time.Now().Unix()
}

func decrementCreativeAssetQuota(quota *CreativeAssetQuota, asset *CreativeAsset) {
	if quota.AssetCount > 0 {
		quota.AssetCount--
	}
	quota.TotalSizeBytes -= asset.SizeBytes
	if quota.TotalSizeBytes < 0 {
		quota.TotalSizeBytes = 0
	}
	if asset.StorageBackend == CreativeAssetStorageDatabase {
		quota.DatabaseSizeBytes -= asset.SizeBytes
		if quota.DatabaseSizeBytes < 0 {
			quota.DatabaseSizeBytes = 0
		}
	}
	quota.UpdatedTime = time.Now().Unix()
}

func saveCreativeAssetQuotaTx(tx *gorm.DB, quota *CreativeAssetQuota) error {
	return tx.Model(&CreativeAssetQuota{}).
		Where("user_id = ?", quota.UserId).
		UpdateColumns(map[string]any{
			"asset_count":         quota.AssetCount,
			"total_size_bytes":    quota.TotalSizeBytes,
			"database_size_bytes": quota.DatabaseSizeBytes,
			"updated_time":        quota.UpdatedTime,
		}).Error
}

func validateCreativeDocumentAssetIdsExistTx(tx *gorm.DB, userId int, assetIds map[string]struct{}) error {
	if len(assetIds) == 0 {
		return nil
	}
	ids := make([]string, 0, len(assetIds))
	for assetId := range assetIds {
		ids = append(ids, assetId)
	}
	var assets []CreativeAsset
	err := activeCreativeAssetQuery(tx.Clauses(clause.Locking{Strength: "UPDATE"}).Model(&CreativeAsset{})).
		Where("user_id = ? AND asset_id IN ?", userId, ids).
		Find(&assets).Error
	if err != nil {
		return err
	}
	if len(assets) != len(ids) {
		return fmt.Errorf("creative asset reference is invalid")
	}
	return nil
}
