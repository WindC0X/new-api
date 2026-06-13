package service

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
)

const (
	MaxCreativeTaskIDListSize = 50
	MaxCreativeTaskIDLength   = 191
)

func NormalizeCreativeTaskIDList(ids []any) ([]any, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	if len(ids) > MaxCreativeTaskIDListSize {
		return nil, fmt.Errorf("too many task ids: max %d", MaxCreativeTaskIDListSize)
	}
	normalized := make([]any, 0, len(ids))
	for index, id := range ids {
		taskID, err := normalizeCreativeTaskID(id)
		if err != nil {
			return nil, fmt.Errorf("invalid task id at index %d: %w", index, err)
		}
		normalized = append(normalized, taskID)
	}
	return normalized, nil
}

func normalizeCreativeTaskID(id any) (string, error) {
	var value string
	switch typed := id.(type) {
	case string:
		value = strings.TrimSpace(typed)
	case json.Number:
		value = strings.TrimSpace(typed.String())
	case int:
		value = strconv.Itoa(typed)
	case int8:
		value = strconv.FormatInt(int64(typed), 10)
	case int16:
		value = strconv.FormatInt(int64(typed), 10)
	case int32:
		value = strconv.FormatInt(int64(typed), 10)
	case int64:
		value = strconv.FormatInt(typed, 10)
	case uint:
		value = strconv.FormatUint(uint64(typed), 10)
	case uint8:
		value = strconv.FormatUint(uint64(typed), 10)
	case uint16:
		value = strconv.FormatUint(uint64(typed), 10)
	case uint32:
		value = strconv.FormatUint(uint64(typed), 10)
	case uint64:
		value = strconv.FormatUint(typed, 10)
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) || typed != math.Trunc(typed) {
			return "", fmt.Errorf("must be a string or integer")
		}
		value = strconv.FormatInt(int64(typed), 10)
	default:
		return "", fmt.Errorf("must be a string or integer")
	}
	if value == "" {
		return "", fmt.Errorf("must not be empty")
	}
	if len(value) > MaxCreativeTaskIDLength {
		return "", fmt.Errorf("length exceeds %d", MaxCreativeTaskIDLength)
	}
	return value, nil
}
