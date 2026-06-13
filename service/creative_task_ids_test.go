package service

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeCreativeTaskIDListAcceptsSafeIDs(t *testing.T) {
	ids, err := NormalizeCreativeTaskIDList([]any{"task_alpha", " task_beta "})

	require.NoError(t, err)
	require.Equal(t, []any{"task_alpha", "task_beta"}, ids)
}

func TestNormalizeCreativeTaskIDListRejectsTooManyIDs(t *testing.T) {
	input := make([]any, MaxCreativeTaskIDListSize+1)
	for i := range input {
		input[i] = "task_overflow"
	}

	_, err := NormalizeCreativeTaskIDList(input)

	require.Error(t, err)
	require.Contains(t, err.Error(), "too many")
}

func TestNormalizeCreativeTaskIDListRejectsLongOrInvalidIDs(t *testing.T) {
	_, longErr := NormalizeCreativeTaskIDList([]any{strings.Repeat("a", MaxCreativeTaskIDLength+1)})
	require.Error(t, longErr)

	_, typeErr := NormalizeCreativeTaskIDList([]any{map[string]any{"id": "task_nested"}})
	require.Error(t, typeErr)
}
