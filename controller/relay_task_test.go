package controller

import (
	"errors"
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/relay"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/stretchr/testify/require"
)

func TestShouldDeleteCreativeTaskIdempotencyOnRelayErrorDeletesWhenAcceptedButNotPersisted(t *testing.T) {
	info := &relaycommon.RelayInfo{
		TaskRelayInfo: &relaycommon.TaskRelayInfo{IdempotencyKey: "creative-request-id"},
	}
	result := &relay.TaskSubmitResult{UpstreamTaskID: "upstream-accepted"}

	require.True(t, shouldDeleteCreativeTaskIdempotencyOnRelayError(info, false, result))
	require.False(t, shouldDeleteCreativeTaskIdempotencyOnRelayError(info, true, result))
}

func TestShouldDeleteCreativeTaskIdempotencyOnRelayErrorDeletesOnlyBeforeUpstreamAccepted(t *testing.T) {
	info := &relaycommon.RelayInfo{
		TaskRelayInfo: &relaycommon.TaskRelayInfo{IdempotencyKey: "creative-request-id"},
	}

	require.True(t, shouldDeleteCreativeTaskIdempotencyOnRelayError(info, false, nil))
	require.False(t, shouldDeleteCreativeTaskIdempotencyOnRelayError(info, true, nil))
}

func TestShouldRefundTaskSubmitBillingRefundsAcceptedTaskWithoutLocalPersistence(t *testing.T) {
	taskErr := &dto.TaskError{Code: "insert_task_failed", StatusCode: http.StatusInternalServerError}
	result := &relay.TaskSubmitResult{UpstreamTaskID: "upstream-accepted"}

	require.True(t, shouldRefundTaskSubmitBilling(taskErr, result, false))
	require.False(t, shouldRefundTaskSubmitBilling(taskErr, result, true))
}

func TestShouldRefundTaskSubmitBillingRefundsBeforeUpstreamAccepted(t *testing.T) {
	taskErr := &dto.TaskError{Code: "upstream_submit_failed", StatusCode: http.StatusBadGateway}

	require.True(t, shouldRefundTaskSubmitBilling(taskErr, nil, false))
	require.False(t, shouldRefundTaskSubmitBilling(nil, nil, false))
}

func TestTaskSubmitSettleFailureErrorFailsClosed(t *testing.T) {
	err := errors.New("outbox enqueue failed")

	taskErr := taskSubmitSettleFailureError(err)

	require.NotNil(t, taskErr)
	require.Equal(t, "settle_task_billing_failed", taskErr.Code)
	require.Equal(t, http.StatusInternalServerError, taskErr.StatusCode)
	require.Nil(t, taskSubmitSettleFailureError(nil))
}
