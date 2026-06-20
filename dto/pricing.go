package dto

import (
	"encoding/json"

	"github.com/QuantumNous/new-api/constant"
)

// 这里不好动就不动了，本来想独立出来的（
type OpenAIModels struct {
	Id                     string                  `json:"id"`
	Object                 string                  `json:"object"`
	Created                int                     `json:"created"`
	OwnedBy                string                  `json:"owned_by"`
	SupportedEndpointTypes []constant.EndpointType `json:"supported_endpoint_types"`
}

type CreativeModelCatalogItem struct {
	Id                     string                        `json:"id"`
	Object                 string                        `json:"object"`
	Created                int                           `json:"created"`
	OwnedBy                string                        `json:"owned_by"`
	SupportedEndpointTypes []constant.EndpointType       `json:"supported_endpoint_types"`
	ProviderModelId        string                        `json:"providerModelId,omitempty"`
	PriceModelId           string                        `json:"priceModelId,omitempty"`
	Label                  string                        `json:"label,omitempty"`
	DisplayName            string                        `json:"displayName,omitempty"`
	ShortLabel             string                        `json:"shortLabel,omitempty"`
	ShortCode              string                        `json:"shortCode,omitempty"`
	Description            string                        `json:"description,omitempty"`
	Type                   string                        `json:"type,omitempty"`
	Modality               string                        `json:"modality,omitempty"`
	Vendor                 string                        `json:"vendor,omitempty"`
	Tags                   []string                      `json:"tags,omitempty"`
	RecommendedScore       *int                          `json:"recommendedScore,omitempty"`
	SortOrder              *int                          `json:"sortOrder,omitempty"`
	ParameterSchema        []CreativeParameterSchemaItem `json:"parameterSchema,omitempty"`
	ParameterSchemaPresent bool                          `json:"-"`
}

func (item CreativeModelCatalogItem) MarshalJSON() ([]byte, error) {
	type alias CreativeModelCatalogItem
	if !item.ParameterSchemaPresent {
		return json.Marshal(alias(item))
	}
	raw, err := json.Marshal(alias(item))
	if err != nil {
		return nil, err
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, err
	}
	schema := item.ParameterSchema
	if schema == nil {
		schema = []CreativeParameterSchemaItem{}
	}
	encodedSchema, err := json.Marshal(schema)
	if err != nil {
		return nil, err
	}
	object["parameterSchema"] = encodedSchema
	return json.Marshal(object)
}

type CreativeParamOption struct {
	Value any    `json:"value"`
	Label string `json:"label"`
}

type CreativeParameterSchemaItem struct {
	Id           string                `json:"id"`
	Label        string                `json:"label"`
	ShortLabel   string                `json:"shortLabel,omitempty"`
	Description  string                `json:"description,omitempty"`
	Type         string                `json:"type"`
	DefaultValue any                   `json:"defaultValue,omitempty"`
	Options      []CreativeParamOption `json:"options,omitempty"`
	Min          *float64              `json:"min,omitempty"`
	Max          *float64              `json:"max,omitempty"`
	Step         *float64              `json:"step,omitempty"`
	Required     bool                  `json:"required,omitempty"`
	Order        int                   `json:"order,omitempty"`
	Hidden       bool                  `json:"hidden,omitempty"`
}

type AnthropicModel struct {
	ID          string `json:"id"`
	CreatedAt   string `json:"created_at"`
	DisplayName string `json:"display_name"`
	Type        string `json:"type"`
}

type GeminiModel struct {
	Name                       interface{}   `json:"name"`
	BaseModelId                interface{}   `json:"baseModelId"`
	Version                    interface{}   `json:"version"`
	DisplayName                interface{}   `json:"displayName"`
	Description                interface{}   `json:"description"`
	InputTokenLimit            interface{}   `json:"inputTokenLimit"`
	OutputTokenLimit           interface{}   `json:"outputTokenLimit"`
	SupportedGenerationMethods []interface{} `json:"supportedGenerationMethods"`
	Thinking                   interface{}   `json:"thinking"`
	Temperature                interface{}   `json:"temperature"`
	MaxTemperature             interface{}   `json:"maxTemperature"`
	TopP                       interface{}   `json:"topP"`
	TopK                       interface{}   `json:"topK"`
}
