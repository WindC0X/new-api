package service

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
)

const (
	CreativeAdapterEnabledOptionKey      = "creative.adapter.enabled"
	CreativeAdapterCanaryGroupsOptionKey = "creative.adapter.canary_groups"
)

var creativeParameterIDPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_:-]{0,63}$`)

var creativeParameterAllowedTypes = map[string]struct{}{
	"enum":    {},
	"string":  {},
	"number":  {},
	"integer": {},
	"boolean": {},
}

var creativeParameterForbiddenFragments = []string{
	"apikey",
	"authorization",
	"bearer",
	"token",
	"secret",
	"key",
	"credential",
	"baseurl",
	"url",
	"endpoint",
	"host",
	"header",
	"channel",
	"provider",
	"modelid",
	"modelname",
	"modelref",
	"sourceprofileid",
	"profileid",
	"internaloptions",
	"onprogress",
	"onsubmitted",
	"idempotency",
	"route",
	"routing",
	"group",
	"user",
	"owner",
	"notifyhook",
	"callback",
	"webhook",
	"mjapisecret",
}

// GetCreativePreviewModelBindingsForGroup returns Phase-A preview bindings only.
// It is fail-closed by default: both the global flag and a canary group match
// are required. These bindings are catalog/schema previews only and are not a
// provider routing contract.
func GetCreativePreviewModelBindingsForGroup(userGroup string) []dto.CreativeModelCatalogItem {
	if !creativeAdapterPreviewEnabled() || !creativeAdapterCanaryGroupAllowed(userGroup) {
		return nil
	}
	binding := mockCreativeImagePreviewBinding()
	if err := ValidateCreativeParameterSchema(binding.ParameterSchema); err != nil {
		common.SysError("invalid built-in creative preview schema: " + err.Error())
		return nil
	}
	return []dto.CreativeModelCatalogItem{binding}
}

func ValidateCreativeParameterSchema(schema []dto.CreativeParameterSchemaItem) error {
	if len(schema) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(schema))
	for _, item := range schema {
		id := strings.TrimSpace(item.Id)
		if id == "" {
			return errors.New("schema item id is required")
		}
		if !creativeParameterIDPattern.MatchString(id) {
			return fmt.Errorf("schema item %q has invalid id", id)
		}
		if creativeParameterIDForbidden(id) {
			return fmt.Errorf("schema item %q uses a forbidden control field", id)
		}
		key := strings.ToLower(id)
		if _, ok := seen[key]; ok {
			return fmt.Errorf("schema item %q is duplicated", id)
		}
		seen[key] = struct{}{}
		if strings.TrimSpace(item.Label) == "" {
			return fmt.Errorf("schema item %q label is required", id)
		}
		paramType := strings.TrimSpace(strings.ToLower(item.Type))
		if _, ok := creativeParameterAllowedTypes[paramType]; !ok {
			return fmt.Errorf("schema item %q has unsupported type %q", id, item.Type)
		}
		if item.DefaultValue != nil {
			if !creativeParameterScalarValue(item.DefaultValue) {
				return fmt.Errorf("schema item %q defaultValue has non-scalar value", id)
			}
			if paramType != "enum" && !creativeParameterDefaultMatchesType(paramType, item.DefaultValue) {
				return fmt.Errorf("schema item %q defaultValue does not match type %q", id, paramType)
			}
		}
		if paramType == "enum" {
			if len(item.Options) == 0 {
				return fmt.Errorf("schema item %q enum requires options", id)
			}
			for _, option := range item.Options {
				if !creativeParameterScalarValue(option.Value) {
					return fmt.Errorf("schema item %q enum option has non-scalar value", id)
				}
			}
			if item.DefaultValue != nil && !creativeParameterOptionContainsValue(item.Options, item.DefaultValue) {
				return fmt.Errorf("schema item %q defaultValue is not in options", id)
			}
		}
	}
	return nil
}

func creativeAdapterPreviewEnabled() bool {
	return strings.EqualFold(strings.TrimSpace(creativeOptionValue(CreativeAdapterEnabledOptionKey)), "true")
}

func creativeAdapterCanaryGroupAllowed(userGroup string) bool {
	allowed := creativeOptionList(CreativeAdapterCanaryGroupsOptionKey)
	if len(allowed) == 0 {
		return false
	}
	group := strings.TrimSpace(userGroup)
	for _, candidate := range allowed {
		if candidate == "*" || candidate == group {
			return true
		}
	}
	return false
}

func creativeOptionValue(key string) string {
	common.OptionMapRWMutex.RLock()
	defer common.OptionMapRWMutex.RUnlock()
	return common.OptionMap[key]
}

func creativeOptionList(key string) []string {
	raw := strings.TrimSpace(creativeOptionValue(key))
	if raw == "" {
		return nil
	}
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		switch r {
		case ',', ';', '|', '\n', '\r', '\t', ' ':
			return true
		default:
			return false
		}
	})
	seen := make(map[string]struct{}, len(parts))
	values := make([]string, 0, len(parts))
	for _, part := range parts {
		value := strings.TrimSpace(part)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		values = append(values, value)
	}
	sort.Strings(values)
	return values
}

func creativeParameterIDForbidden(id string) bool {
	normalized := strings.ToLower(strings.TrimSpace(id))
	normalized = strings.NewReplacer("_", "", "-", "", ":", "", " ", "").Replace(normalized)
	for _, fragment := range creativeParameterForbiddenFragments {
		if strings.Contains(normalized, fragment) {
			return true
		}
	}
	return false
}

func creativeParameterDefaultMatchesType(paramType string, value any) bool {
	switch paramType {
	case "string":
		_, ok := value.(string)
		return ok
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "number":
		return creativeParameterNumericValue(value)
	case "integer":
		return creativeParameterIntegerValue(value)
	default:
		return false
	}
}

func creativeParameterNumericValue(value any) bool {
	_, _, ok := creativeParameterNumber(value)
	return ok
}

func creativeParameterIntegerValue(value any) bool {
	number, integral, ok := creativeParameterNumber(value)
	return ok && (integral || math.Trunc(number) == number)
}

func creativeParameterNumber(value any) (float64, bool, bool) {
	switch typed := value.(type) {
	case int:
		return float64(typed), true, true
	case int8:
		return float64(typed), true, true
	case int16:
		return float64(typed), true, true
	case int32:
		return float64(typed), true, true
	case int64:
		return float64(typed), true, true
	case uint:
		return float64(typed), true, true
	case uint8:
		return float64(typed), true, true
	case uint16:
		return float64(typed), true, true
	case uint32:
		return float64(typed), true, true
	case uint64:
		return float64(typed), true, true
	case float32:
		number := float64(typed)
		return number, false, !math.IsNaN(number) && !math.IsInf(number, 0)
	case float64:
		return typed, false, !math.IsNaN(typed) && !math.IsInf(typed, 0)
	default:
		return 0, false, false
	}
}

func creativeParameterOptionContainsValue(options []dto.CreativeParamOption, value any) bool {
	needleKind, needleValue, ok := creativeParameterComparableValue(value)
	if !ok {
		return false
	}
	for _, option := range options {
		kind, comparableValue, ok := creativeParameterComparableValue(option.Value)
		if ok && kind == needleKind && comparableValue == needleValue {
			return true
		}
	}
	return false
}

func creativeParameterScalarValue(value any) bool {
	_, _, ok := creativeParameterComparableValue(value)
	return ok
}

func creativeParameterComparableValue(value any) (string, string, bool) {
	switch typed := value.(type) {
	case string:
		return "string", typed, true
	case bool:
		return "bool", fmt.Sprintf("%t", typed), true
	case int:
		return "number", fmt.Sprintf("%d", typed), true
	case int8:
		return "number", fmt.Sprintf("%d", typed), true
	case int16:
		return "number", fmt.Sprintf("%d", typed), true
	case int32:
		return "number", fmt.Sprintf("%d", typed), true
	case int64:
		return "number", fmt.Sprintf("%d", typed), true
	case uint:
		return "number", fmt.Sprintf("%d", typed), true
	case uint8:
		return "number", fmt.Sprintf("%d", typed), true
	case uint16:
		return "number", fmt.Sprintf("%d", typed), true
	case uint32:
		return "number", fmt.Sprintf("%d", typed), true
	case uint64:
		return "number", fmt.Sprintf("%d", typed), true
	case float32:
		number := float64(typed)
		if math.IsNaN(number) || math.IsInf(number, 0) {
			return "", "", false
		}
		return "number", fmt.Sprintf("%.9g", typed), true
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) {
			return "", "", false
		}
		return "number", fmt.Sprintf("%.17g", typed), true
	default:
		return "", "", false
	}
}

func mockCreativeImagePreviewBinding() dto.CreativeModelCatalogItem {
	recommendedScore := 10
	sortOrder := 1000
	return dto.CreativeModelCatalogItem{
		Id:                     "mock:gpt-image-2:preview",
		Object:                 "model",
		Created:                0,
		OwnedBy:                "new-api-creative",
		SupportedEndpointTypes: []constant.EndpointType{constant.EndpointTypeImageGeneration},
		ProviderModelId:        "gpt-image-2",
		PriceModelId:           "mock-gpt-image-2-price",
		Label:                  "GPT Image 2 · Mock Preview",
		DisplayName:            "GPT Image 2 · Mock Preview",
		ShortLabel:             "Mock GPT Image 2",
		ShortCode:              "mgpt2",
		Description:            "Mock-only Creative adapter preview binding. It is not routed to any provider.",
		Type:                   "image",
		Modality:               "image",
		Vendor:                 "new-api-creative",
		Tags:                   []string{"creative-adapter", "mock", "preview", "image"},
		RecommendedScore:       &recommendedScore,
		SortOrder:              &sortOrder,
		ParameterSchema: []dto.CreativeParameterSchemaItem{
			{
				Id:           "size",
				Label:        "Size",
				ShortLabel:   "Size",
				Description:  "Mock preview image size.",
				Type:         "enum",
				DefaultValue: "1024x1024",
				Options: []dto.CreativeParamOption{
					{Value: "1024x1024", Label: "1024×1024"},
					{Value: "16:9", Label: "16:9"},
				},
				Order: 10,
			},
			{
				Id:           "quality",
				Label:        "Quality",
				ShortLabel:   "Quality",
				Description:  "Mock preview quality.",
				Type:         "enum",
				DefaultValue: "auto",
				Options: []dto.CreativeParamOption{
					{Value: "auto", Label: "Auto"},
					{Value: "high", Label: "High"},
				},
				Order: 20,
			},
		},
	}
}
