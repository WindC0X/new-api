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
	CreativeDocumentAssetRefLimit    = 1000
)

var creativeAssetIdPattern = regexp.MustCompile(`^asset_[A-Za-z0-9_-]{6,122}$`)

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
	CreatedTime      int64  `json:"created_time" gorm:"bigint;index"`
	UpdatedTime      int64  `json:"updated_time" gorm:"bigint;index"`
	LastAccessedTime int64  `json:"last_accessed_time" gorm:"bigint;index"`
}

func (asset *CreativeAsset) BeforeSave(tx *gorm.DB) error {
	asset.AssetId = strings.TrimSpace(asset.AssetId)
	asset.ContentHash = strings.TrimSpace(asset.ContentHash)
	asset.MediaType = strings.TrimSpace(asset.MediaType)
	asset.MimeType = strings.TrimSpace(asset.MimeType)
	asset.StorageBackend = strings.TrimSpace(asset.StorageBackend)
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
	err := DB.Where("user_id = ? AND asset_id = ?", userId, strings.TrimSpace(assetId)).First(&asset).Error
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
	err := DB.Where("user_id = ? AND content_hash = ?", userId, strings.TrimSpace(contentHash)).First(&asset).Error
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
	return DB.Model(&CreativeAsset{}).
		Where("user_id = ? AND asset_id = ?", userId, assetId).
		Update("last_accessed_time", time.Now().Unix()).Error
}

func DeleteCreativeAssetMetadata(userId int, assetId string) error {
	return DB.Where("user_id = ? AND asset_id = ?", userId, assetId).Delete(&CreativeAsset{}).Error
}

func CountCreativeAssetRefs(userId int, assetId string) (int64, error) {
	var count int64
	err := DB.Model(&CreativeDocumentAssetRef{}).Where("user_id = ? AND asset_id = ?", userId, assetId).Count(&count).Error
	return count, err
}

func RefreshCreativeDocumentAssetRefs(userId int, documentId string, assetIds []string) error {
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

	return DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("user_id = ? AND document_id = ?", userId, documentId).Delete(&CreativeDocumentAssetRef{}).Error; err != nil {
			return err
		}
		if len(refs) == 0 {
			return nil
		}
		return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&refs).Error
	})
}

func DeleteCreativeDocumentAssetRefs(userId int, documentId string) error {
	return DB.Where("user_id = ? AND document_id = ?", userId, strings.TrimSpace(documentId)).Delete(&CreativeDocumentAssetRef{}).Error
}

func ListCreativeDocumentAssetRefs(userId int, documentId string) ([]CreativeDocumentAssetRef, error) {
	var refs []CreativeDocumentAssetRef
	err := DB.Where("user_id = ? AND document_id = ?", userId, strings.TrimSpace(documentId)).
		Order("asset_id ASC").Find(&refs).Error
	return refs, err
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
	err := DB.Model(&CreativeAsset{}).Where("user_id = ? AND asset_id = ?", userId, assetId).Count(&count).Error
	return count > 0, err
}
