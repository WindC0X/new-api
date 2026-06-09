package model

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	creativeDefaultPreferenceJSON = "{}"
	creativeDefaultSnapshotJSON   = "{}"
	creativeDefaultMetadataJSON   = "{}"
	creativeStringListLimit       = 512
	creativeStringValueLimit      = 512
)

// CreativeModelPreference stores only allowlisted opentu model preference state.
// It intentionally stores the preference as a validated JSON string so secrets or
// provider settings are not represented as columns and future schema additions
// can remain backward-compatible.
type CreativeModelPreference struct {
	Id             int    `json:"id" gorm:"primaryKey"`
	UserId         int    `json:"user_id" gorm:"uniqueIndex;not null"`
	PreferenceJSON string `json:"preference_json" gorm:"type:text;not null"`
	Revision       int    `json:"revision" gorm:"not null;default:0;index"`
	CreatedTime    int64  `json:"created_time" gorm:"bigint"`
	UpdatedTime    int64  `json:"updated_time" gorm:"bigint"`
}

// CreativeModelSafeSelection is the allowlisted model selection shape shared
// with opentu preference sync. Do not add provider/API-key/base URL/channel
// override fields here.
type CreativeModelSafeSelection struct {
	ModelId        string `json:"modelId"`
	ProfileId      string `json:"profileId,omitempty"`
	ProviderIdHint string `json:"providerIdHint,omitempty"`
	VendorHint     string `json:"vendorHint,omitempty"`
	UpdatedAt      *int64 `json:"updatedAt,omitempty"`
}

// CreativeModelPreferenceValue is the complete safe cloud-sync schema for model
// preferences. Do not add provider/API-key/base URL/channel override fields here.
type CreativeModelPreferenceValue struct {
	Default     map[string]CreativeModelSafeSelection `json:"default,omitempty"`
	Pinned      []CreativeModelSafeSelection          `json:"pinned,omitempty"`
	Recent      []CreativeModelSafeSelection          `json:"recent,omitempty"`
	DisplayMode string                                `json:"displayMode,omitempty"`
	CustomOrder []string                              `json:"customOrder,omitempty"`
}

// CreativeDocument stores one user's local-first opentu document snapshot.
type CreativeDocument struct {
	UserId           int    `json:"user_id" gorm:"primaryKey;autoIncrement:false"`
	DocumentId       string `json:"id" gorm:"column:document_id;type:varchar(128);primaryKey;autoIncrement:false"`
	Title            string `json:"title" gorm:"type:varchar(255);default:''"`
	SnapshotJSON     string `json:"snapshot_json" gorm:"type:text;not null"`
	MetadataJSON     string `json:"metadata_json" gorm:"type:text;not null"`
	Revision         int    `json:"revision" gorm:"not null;default:1;index"`
	ClientMutationId string `json:"client_mutation_id" gorm:"type:varchar(128);index"`
	CreatedTime      int64  `json:"created_time" gorm:"bigint;index"`
	UpdatedTime      int64  `json:"updated_time" gorm:"bigint;index"`
}

// CreativeDocumentPatch is the safe mutation shape accepted by the model layer.
type CreativeDocumentPatch struct {
	Title            *string
	SnapshotJSON     *string
	MetadataJSON     *string
	ClientMutationId string
}

func GetCreativeModelPreference(userId int) (*CreativeModelPreference, bool, error) {
	var preference CreativeModelPreference
	err := DB.Where("user_id = ?", userId).First(&preference).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return &CreativeModelPreference{
			UserId:         userId,
			PreferenceJSON: creativeDefaultPreferenceJSON,
			Revision:       0,
		}, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if strings.TrimSpace(preference.PreferenceJSON) == "" {
		preference.PreferenceJSON = creativeDefaultPreferenceJSON
	}
	return &preference, true, nil
}

func PatchCreativeModelPreference(userId int, baseRevision int, value CreativeModelPreferenceValue) (*CreativeModelPreference, bool, error) {
	preferenceJSON, err := creativePreferenceValueToJSON(value)
	if err != nil {
		return nil, false, err
	}

	var result *CreativeModelPreference
	conflict := false
	now := time.Now().Unix()
	err = DB.Transaction(func(tx *gorm.DB) error {
		var current CreativeModelPreference
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id = ?", userId).First(&current).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if baseRevision != 0 {
				result = &CreativeModelPreference{UserId: userId, PreferenceJSON: creativeDefaultPreferenceJSON, Revision: 0}
				conflict = true
				return nil
			}
			created := CreativeModelPreference{
				UserId:         userId,
				PreferenceJSON: preferenceJSON,
				Revision:       1,
				CreatedTime:    now,
				UpdatedTime:    now,
			}
			if err := tx.Create(&created).Error; err != nil {
				return err
			}
			result = &created
			return nil
		}
		if err != nil {
			return err
		}
		if current.Revision != baseRevision {
			if strings.TrimSpace(current.PreferenceJSON) == "" {
				current.PreferenceJSON = creativeDefaultPreferenceJSON
			}
			result = &current
			conflict = true
			return nil
		}
		current.PreferenceJSON = preferenceJSON
		current.Revision++
		current.UpdatedTime = now
		if current.CreatedTime == 0 {
			current.CreatedTime = now
		}
		if err := tx.Save(&current).Error; err != nil {
			return err
		}
		result = &current
		return nil
	})
	return result, conflict, err
}

func SanitizeCreativeModelPreference(input map[string]any) (CreativeModelPreferenceValue, error) {
	if input == nil {
		return CreativeModelPreferenceValue{}, nil
	}
	return CreativeModelPreferenceValue{
		Default:     sanitizeCreativeSelectionMap(input["default"]),
		Pinned:      sanitizeCreativeSelectionList(input["pinned"]),
		Recent:      sanitizeCreativeSelectionList(input["recent"]),
		DisplayMode: sanitizeCreativeString(input["displayMode"]),
		CustomOrder: sanitizeCreativeStringList(input["customOrder"]),
	}, nil
}

func CreativeModelPreferenceValueFromJSON(jsonText string) (CreativeModelPreferenceValue, error) {
	if strings.TrimSpace(jsonText) == "" {
		return CreativeModelPreferenceValue{}, nil
	}
	var raw any
	if err := common.Unmarshal([]byte(jsonText), &raw); err != nil {
		return CreativeModelPreferenceValue{}, err
	}
	if raw == nil {
		return CreativeModelPreferenceValue{}, nil
	}
	rawMap, ok := raw.(map[string]any)
	if !ok {
		return CreativeModelPreferenceValue{}, fmt.Errorf("creative model preference must be an object")
	}
	return SanitizeCreativeModelPreference(rawMap)
}

func creativePreferenceValueToJSON(value CreativeModelPreferenceValue) (string, error) {
	value = CreativeModelPreferenceValue{
		Default:     sanitizeCreativeSelectionMap(value.Default),
		Pinned:      sanitizeCreativeSelectionList(value.Pinned),
		Recent:      sanitizeCreativeSelectionList(value.Recent),
		DisplayMode: sanitizeCreativeString(value.DisplayMode),
		CustomOrder: sanitizeCreativeStringList(value.CustomOrder),
	}
	encoded, err := common.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func sanitizeCreativeSelectionMap(value any) map[string]CreativeModelSafeSelection {
	result := map[string]CreativeModelSafeSelection{}
	addSelection := func(rawKey string, rawSelection any) {
		key := sanitizeCreativeString(rawKey)
		if key == "" {
			return
		}
		selection, ok := sanitizeCreativeSafeSelection(rawSelection)
		if !ok {
			return
		}
		result[key] = selection
	}

	switch typed := value.(type) {
	case nil:
		return nil
	case string:
		selection, ok := sanitizeCreativeSafeSelection(typed)
		if ok {
			result["text"] = selection
		}
	case map[string]CreativeModelSafeSelection:
		for key, selection := range typed {
			addSelection(key, selection)
			if len(result) >= creativeStringListLimit {
				break
			}
		}
	case map[string]*CreativeModelSafeSelection:
		for key, selection := range typed {
			if selection != nil {
				addSelection(key, *selection)
			}
			if len(result) >= creativeStringListLimit {
				break
			}
		}
	case map[string]any:
		for key, selection := range typed {
			addSelection(key, selection)
			if len(result) >= creativeStringListLimit {
				break
			}
		}
	default:
		return nil
	}

	if len(result) == 0 {
		return nil
	}
	return result
}

func sanitizeCreativeSelectionList(value any) []CreativeModelSafeSelection {
	result := make([]CreativeModelSafeSelection, 0)
	addSelection := func(rawSelection any) {
		selection, ok := sanitizeCreativeSafeSelection(rawSelection)
		if !ok {
			return
		}
		result = append(result, selection)
	}

	switch list := value.(type) {
	case nil:
		return nil
	case []CreativeModelSafeSelection:
		result = make([]CreativeModelSafeSelection, 0, len(list))
		for _, item := range list {
			addSelection(item)
			if len(result) >= creativeStringListLimit {
				break
			}
		}
	case []*CreativeModelSafeSelection:
		result = make([]CreativeModelSafeSelection, 0, len(list))
		for _, item := range list {
			if item != nil {
				addSelection(*item)
			}
			if len(result) >= creativeStringListLimit {
				break
			}
		}
	case []string:
		result = make([]CreativeModelSafeSelection, 0, len(list))
		for _, item := range list {
			addSelection(item)
			if len(result) >= creativeStringListLimit {
				break
			}
		}
	case []any:
		result = make([]CreativeModelSafeSelection, 0, len(list))
		for _, item := range list {
			addSelection(item)
			if len(result) >= creativeStringListLimit {
				break
			}
		}
	default:
		return nil
	}

	if len(result) == 0 {
		return nil
	}
	return result
}

func sanitizeCreativeSafeSelection(value any) (CreativeModelSafeSelection, bool) {
	var selection CreativeModelSafeSelection
	switch typed := value.(type) {
	case CreativeModelSafeSelection:
		selection = CreativeModelSafeSelection{
			ModelId:        sanitizeCreativeString(typed.ModelId),
			ProfileId:      sanitizeCreativeString(typed.ProfileId),
			ProviderIdHint: sanitizeCreativeString(typed.ProviderIdHint),
			VendorHint:     sanitizeCreativeString(typed.VendorHint),
			UpdatedAt:      sanitizeCreativeUpdatedAt(typed.UpdatedAt),
		}
	case *CreativeModelSafeSelection:
		if typed == nil {
			return CreativeModelSafeSelection{}, false
		}
		return sanitizeCreativeSafeSelection(*typed)
	case string:
		selection = CreativeModelSafeSelection{ModelId: sanitizeCreativeString(typed)}
	case map[string]string:
		selection = CreativeModelSafeSelection{
			ModelId:        sanitizeCreativeString(typed["modelId"]),
			ProfileId:      sanitizeCreativeString(typed["profileId"]),
			ProviderIdHint: sanitizeCreativeString(typed["providerIdHint"]),
			VendorHint:     sanitizeCreativeString(typed["vendorHint"]),
		}
	case map[string]any:
		selection = CreativeModelSafeSelection{
			ModelId:        sanitizeCreativeString(typed["modelId"]),
			ProfileId:      sanitizeCreativeString(typed["profileId"]),
			ProviderIdHint: sanitizeCreativeString(typed["providerIdHint"]),
			VendorHint:     sanitizeCreativeString(typed["vendorHint"]),
			UpdatedAt:      sanitizeCreativeUpdatedAt(typed["updatedAt"]),
		}
	default:
		return CreativeModelSafeSelection{}, false
	}
	if selection.ModelId == "" {
		return CreativeModelSafeSelection{}, false
	}
	return selection, true
}

func sanitizeCreativeUpdatedAt(value any) *int64 {
	switch typed := value.(type) {
	case nil:
		return nil
	case *int64:
		if typed == nil {
			return nil
		}
		updatedAt := *typed
		return &updatedAt
	case int:
		updatedAt := int64(typed)
		return &updatedAt
	case int8:
		updatedAt := int64(typed)
		return &updatedAt
	case int16:
		updatedAt := int64(typed)
		return &updatedAt
	case int32:
		updatedAt := int64(typed)
		return &updatedAt
	case int64:
		updatedAt := typed
		return &updatedAt
	case uint:
		if uint64(typed) > uint64(math.MaxInt64) {
			return nil
		}
		updatedAt := int64(typed)
		return &updatedAt
	case uint8:
		updatedAt := int64(typed)
		return &updatedAt
	case uint16:
		updatedAt := int64(typed)
		return &updatedAt
	case uint32:
		updatedAt := int64(typed)
		return &updatedAt
	case uint64:
		if typed > uint64(math.MaxInt64) {
			return nil
		}
		updatedAt := int64(typed)
		return &updatedAt
	case float32:
		return sanitizeCreativeUpdatedAt(float64(typed))
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) || typed != math.Trunc(typed) || typed < float64(math.MinInt64) || typed >= float64(math.MaxInt64) {
			return nil
		}
		updatedAt := int64(typed)
		return &updatedAt
	default:
		return nil
	}
}

func sanitizeCreativeString(value any) string {
	text, ok := value.(string)
	if !ok {
		return ""
	}
	text = strings.TrimSpace(text)
	if len(text) > creativeStringValueLimit {
		text = text[:creativeStringValueLimit]
	}
	return text
}

func sanitizeCreativeStringList(value any) []string {
	var raw []string
	switch list := value.(type) {
	case []string:
		raw = list
	case []any:
		raw = make([]string, 0, len(list))
		for _, item := range list {
			if s, ok := item.(string); ok {
				raw = append(raw, s)
			}
		}
	default:
		return nil
	}
	seen := make(map[string]struct{}, len(raw))
	result := make([]string, 0, len(raw))
	for _, item := range raw {
		text := sanitizeCreativeString(item)
		if text == "" {
			continue
		}
		if _, ok := seen[text]; ok {
			continue
		}
		seen[text] = struct{}{}
		result = append(result, text)
		if len(result) >= creativeStringListLimit {
			break
		}
	}
	return result
}

func NormalizeCreativeJSONValue(value any, defaultJSON string) (string, error) {
	if strings.TrimSpace(defaultJSON) == "" {
		defaultJSON = "{}"
	}
	if value == nil {
		return defaultJSON, nil
	}
	if text, ok := value.(string); ok {
		text = strings.TrimSpace(text)
		if text == "" {
			return defaultJSON, nil
		}
		var decoded any
		if err := common.Unmarshal([]byte(text), &decoded); err != nil {
			return "", fmt.Errorf("invalid JSON string: %w", err)
		}
		encoded, err := common.Marshal(decoded)
		if err != nil {
			return "", err
		}
		return string(encoded), nil
	}

	encoded, err := common.Marshal(value)
	if err != nil {
		return "", err
	}
	var decoded any
	if err := common.Unmarshal(encoded, &decoded); err != nil {
		return "", err
	}
	canonical, err := common.Marshal(decoded)
	if err != nil {
		return "", err
	}
	return string(canonical), nil
}

func GetCreativeDocument(userId int, documentId string) (*CreativeDocument, bool, error) {
	var document CreativeDocument
	err := DB.Where("user_id = ? AND document_id = ?", userId, documentId).First(&document).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return &document, true, nil
}

func ListCreativeDocuments(userId int) ([]CreativeDocument, error) {
	var documents []CreativeDocument
	err := DB.Where("user_id = ?", userId).Order("updated_time DESC, document_id ASC").Find(&documents).Error
	return documents, err
}

func GetCreativeDocumentByClientMutationId(userId int, clientMutationId string) (*CreativeDocument, bool, error) {
	if strings.TrimSpace(clientMutationId) == "" {
		return nil, false, nil
	}
	var document CreativeDocument
	err := DB.Where("user_id = ? AND client_mutation_id = ?", userId, clientMutationId).First(&document).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return &document, true, nil
}

func CreateCreativeDocument(document *CreativeDocument) (*CreativeDocument, error) {
	if document == nil {
		return nil, errors.New("document is nil")
	}
	if existing, exists, err := GetCreativeDocumentByClientMutationId(document.UserId, document.ClientMutationId); err != nil || exists {
		return existing, err
	}
	if strings.TrimSpace(document.DocumentId) == "" {
		document.DocumentId = fmt.Sprintf("doc_%d_%s", time.Now().UnixNano(), common.GetRandomString(8))
	}
	if document.Revision <= 0 {
		document.Revision = 1
	}
	now := time.Now().Unix()
	if document.CreatedTime == 0 {
		document.CreatedTime = now
	}
	document.UpdatedTime = now
	if err := ensureCreativeDocumentJSON(document); err != nil {
		return nil, err
	}
	if err := DB.Create(document).Error; err != nil {
		return nil, err
	}
	return document, nil
}

func UpdateCreativeDocumentSnapshot(userId int, documentId string, baseRevision int, patch CreativeDocumentPatch) (*CreativeDocument, bool, error) {
	var result *CreativeDocument
	conflict := false
	now := time.Now().Unix()
	err := DB.Transaction(func(tx *gorm.DB) error {
		var current CreativeDocument
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id = ? AND document_id = ?", userId, documentId).First(&current).Error
		if err != nil {
			return err
		}
		if patch.ClientMutationId != "" && current.ClientMutationId == patch.ClientMutationId {
			result = &current
			return nil
		}
		if current.Revision != baseRevision {
			result = &current
			conflict = true
			return nil
		}
		if patch.Title != nil {
			current.Title = strings.TrimSpace(*patch.Title)
			if len(current.Title) > 255 {
				current.Title = current.Title[:255]
			}
		}
		if patch.SnapshotJSON != nil {
			current.SnapshotJSON = *patch.SnapshotJSON
		}
		if patch.MetadataJSON != nil {
			current.MetadataJSON = *patch.MetadataJSON
		}
		current.ClientMutationId = strings.TrimSpace(patch.ClientMutationId)
		current.Revision++
		current.UpdatedTime = now
		if current.CreatedTime == 0 {
			current.CreatedTime = now
		}
		if err := ensureCreativeDocumentJSON(&current); err != nil {
			return err
		}
		if err := tx.Save(&current).Error; err != nil {
			return err
		}
		result = &current
		return nil
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, err
	}
	return result, conflict, err
}

func DeleteCreativeDocument(userId int, documentId string, baseRevision *int) (*CreativeDocument, bool, bool, error) {
	var deleted *CreativeDocument
	conflict := false
	found := false
	err := DB.Transaction(func(tx *gorm.DB) error {
		var current CreativeDocument
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id = ? AND document_id = ?", userId, documentId).First(&current).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		found = true
		deleted = &current
		if baseRevision != nil && current.Revision != *baseRevision {
			conflict = true
			return nil
		}
		return tx.Delete(&current).Error
	})
	return deleted, found, conflict, err
}

func ensureCreativeDocumentJSON(document *CreativeDocument) error {
	if document == nil {
		return errors.New("document is nil")
	}
	snapshot, err := NormalizeCreativeJSONValue(document.SnapshotJSON, creativeDefaultSnapshotJSON)
	if err != nil {
		return fmt.Errorf("invalid snapshot: %w", err)
	}
	metadata, err := NormalizeCreativeJSONValue(document.MetadataJSON, creativeDefaultMetadataJSON)
	if err != nil {
		return fmt.Errorf("invalid metadata: %w", err)
	}
	document.SnapshotJSON = snapshot
	document.MetadataJSON = metadata
	return nil
}
