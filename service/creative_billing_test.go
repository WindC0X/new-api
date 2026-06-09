package service

import (
	"context"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

var (
	creativeBillingMigrateOnce sync.Once
	creativeBillingMigrateErr  error
)

func TestCreativeSessionBillingWalletPreconsumeSettleSkipsTokenQuota(t *testing.T) {
	cleanupCreativeBillingTestRows(t)
	seedCreativeBillingUser(t, 8301, 500)
	seedCreativeBillingToken(t, 9301, 8301, "creative-real-wallet-token", 777, 3)

	ctx := newCreativeBillingTestContext()
	relayInfo := creativeBillingRelayInfo(8301, "creative-wallet-settle", "")

	apiErr := PreConsumeBilling(ctx, 120, relayInfo)
	require.Nil(t, apiErr)
	require.NotNil(t, relayInfo.Billing)
	require.Equal(t, BillingSourceWallet, relayInfo.BillingSource)
	require.Equal(t, 120, relayInfo.FinalPreConsumedQuota)
	requireCreativeBillingUserQuota(t, 8301, 380)
	requireCreativeBillingTokenUnchanged(t, 9301, 777, 3)
	requireCreativeBillingTokenCount(t, 8301, 1)

	require.NoError(t, SettleBilling(ctx, relayInfo, 150))
	requireCreativeBillingUserQuota(t, 8301, 350)
	requireCreativeBillingTokenUnchanged(t, 9301, 777, 3)
	requireCreativeBillingTokenCount(t, 8301, 1)
	require.False(t, relayInfo.Billing.NeedsRefund())
}

func TestCreativeSessionBillingWalletRefundSkipsTokenQuota(t *testing.T) {
	cleanupCreativeBillingTestRows(t)
	seedCreativeBillingUser(t, 8302, 500)
	seedCreativeBillingToken(t, 9302, 8302, "creative-real-wallet-refund-token", 777, 3)

	ctx := newCreativeBillingTestContext()
	relayInfo := creativeBillingRelayInfo(8302, "creative-wallet-refund", "")

	apiErr := PreConsumeBilling(ctx, 120, relayInfo)
	require.Nil(t, apiErr)
	require.True(t, relayInfo.Billing.NeedsRefund())
	requireCreativeBillingUserQuota(t, 8302, 380)
	requireCreativeBillingTokenUnchanged(t, 9302, 777, 3)

	relayInfo.Billing.Refund(ctx)

	require.Eventually(t, func() bool {
		quota, err := model.GetUserQuota(8302, true)
		return err == nil && quota == 500
	}, time.Second, 10*time.Millisecond)
	requireCreativeBillingTokenUnchanged(t, 9302, 777, 3)
	requireCreativeBillingTokenCount(t, 8302, 1)
}

func TestCreativeSessionBillingUpstreamErrorRefundsPreconsumeExactlyOnceAndSkipsTokenQuota(t *testing.T) {
	cleanupCreativeBillingTestRows(t)
	seedCreativeBillingUser(t, 8310, 500)
	seedCreativeBillingToken(t, 9310, 8310, "creative-real-upstream-error-token", 777, 3)

	ctx := newCreativeBillingTestContext()
	relayInfo := creativeBillingRelayInfo(8310, "creative-wallet-upstream-error", "wallet_only")

	apiErr := PreConsumeBilling(ctx, 120, relayInfo)
	require.Nil(t, apiErr)
	require.True(t, relayInfo.Billing.NeedsRefund())
	requireCreativeBillingUserQuota(t, 8310, 380)
	requireCreativeBillingTokenUnchanged(t, 9310, 777, 3)

	// Simulate the relay/controller upstream-error path calling Refund after
	// successful preconsume. Repeated calls must not double-credit the wallet.
	relayInfo.Billing.Refund(ctx)
	relayInfo.Billing.Refund(ctx)

	require.Eventually(t, func() bool {
		quota, err := model.GetUserQuota(8310, true)
		return err == nil && quota == 500
	}, time.Second, 10*time.Millisecond)
	requireCreativeBillingUserQuota(t, 8310, 500)
	requireCreativeBillingTokenUnchanged(t, 9310, 777, 3)
	requireCreativeBillingTokenCount(t, 8310, 1)
	require.False(t, relayInfo.Billing.NeedsRefund())

	relayInfo.Billing.Refund(ctx)
	requireCreativeBillingUserQuota(t, 8310, 500)
	requireCreativeBillingTokenUnchanged(t, 9310, 777, 3)
}

func TestCreativeSessionBillingSubscriptionPreconsumeSettleSkipsTokenQuota(t *testing.T) {
	cleanupCreativeBillingTestRows(t)
	seedCreativeBillingUser(t, 8303, 0)
	seedCreativeBillingToken(t, 9303, 8303, "creative-real-subscription-token", 777, 3)
	seedCreativeBillingSubscription(t, 9403, 9503, 8303, 500, 0)

	ctx := newCreativeBillingTestContext()
	relayInfo := creativeBillingRelayInfo(8303, "creative-subscription-settle", "subscription_only")

	apiErr := PreConsumeBilling(ctx, 200, relayInfo)
	require.Nil(t, apiErr)
	require.NotNil(t, relayInfo.Billing)
	require.Equal(t, BillingSourceSubscription, relayInfo.BillingSource)
	require.Equal(t, 200, relayInfo.FinalPreConsumedQuota)
	require.Equal(t, 9403, relayInfo.SubscriptionId)
	requireCreativeBillingUserQuota(t, 8303, 0)
	requireCreativeBillingSubscriptionUsed(t, 9403, 200)
	requireCreativeBillingTokenUnchanged(t, 9303, 777, 3)
	requireCreativeBillingTokenCount(t, 8303, 1)

	require.NoError(t, SettleBilling(ctx, relayInfo, 150))
	requireCreativeBillingSubscriptionUsed(t, 9403, 150)
	requireCreativeBillingUserQuota(t, 8303, 0)
	requireCreativeBillingTokenUnchanged(t, 9303, 777, 3)
	requireCreativeBillingTokenCount(t, 8303, 1)
	require.False(t, relayInfo.Billing.NeedsRefund())
}

func TestCreativeSessionBillingRejectsInsufficientWalletDespitePlaygroundToken(t *testing.T) {
	cleanupCreativeBillingTestRows(t)
	seedCreativeBillingUser(t, 8304, 10)
	seedCreativeBillingToken(t, 9304, 8304, "creative-real-insufficient-token", 777, 3)

	ctx := newCreativeBillingTestContext()
	relayInfo := creativeBillingRelayInfo(8304, "creative-wallet-insufficient", "wallet_only")

	apiErr := PreConsumeBilling(ctx, 50, relayInfo)

	require.NotNil(t, apiErr)
	require.Equal(t, types.ErrorCodeInsufficientUserQuota, apiErr.GetErrorCode())
	require.Nil(t, relayInfo.Billing)
	requireCreativeBillingUserQuota(t, 8304, 10)
	requireCreativeBillingTokenUnchanged(t, 9304, 777, 3)
	requireCreativeBillingTokenCount(t, 8304, 1)
}

func TestCreativeSessionBillingZeroUsageSettleRefundsWalletAndSkipsTokenQuota(t *testing.T) {
	cleanupCreativeBillingTestRows(t)
	seedCreativeBillingUser(t, 8305, 500)
	seedCreativeBillingToken(t, 9305, 8305, "creative-real-zero-usage-token", 777, 3)

	ctx := newCreativeBillingTestContext()
	relayInfo := creativeBillingRelayInfo(8305, "creative-wallet-zero-usage", "wallet_only")

	apiErr := PreConsumeBilling(ctx, 120, relayInfo)
	require.Nil(t, apiErr)
	require.Equal(t, BillingSourceWallet, relayInfo.BillingSource)
	requireCreativeBillingUserQuota(t, 8305, 380)
	requireCreativeBillingTokenUnchanged(t, 9305, 777, 3)

	require.NoError(t, SettleBilling(ctx, relayInfo, 0))
	requireCreativeBillingUserQuota(t, 8305, 500)
	requireCreativeBillingTokenUnchanged(t, 9305, 777, 3)
	requireCreativeBillingTokenCount(t, 8305, 1)
	require.False(t, relayInfo.Billing.NeedsRefund())

	relayInfo.Billing.Refund(ctx)
	requireCreativeBillingUserQuota(t, 8305, 500)
	requireCreativeBillingTokenUnchanged(t, 9305, 777, 3)
}

func TestCreativeSessionBillingSubscriptionZeroUsageSettleThenRefundDoesNotDoubleCredit(t *testing.T) {
	cleanupCreativeBillingTestRows(t)
	seedCreativeBillingUser(t, 8311, 0)
	seedCreativeBillingToken(t, 9311, 8311, "creative-real-subscription-zero-token", 777, 3)
	seedCreativeBillingSubscription(t, 9411, 9511, 8311, 500, 80)

	ctx := newCreativeBillingTestContext()
	relayInfo := creativeBillingRelayInfo(8311, "creative-subscription-zero-usage", "subscription_only")

	apiErr := PreConsumeBilling(ctx, 120, relayInfo)
	require.Nil(t, apiErr)
	require.Equal(t, BillingSourceSubscription, relayInfo.BillingSource)
	require.Equal(t, 9411, relayInfo.SubscriptionId)
	requireCreativeBillingSubscriptionUsed(t, 9411, 200)
	requireCreativeBillingTokenUnchanged(t, 9311, 777, 3)

	require.NoError(t, SettleBilling(ctx, relayInfo, 0))
	requireCreativeBillingSubscriptionUsed(t, 9411, 80)
	requireCreativeBillingTokenUnchanged(t, 9311, 777, 3)
	require.False(t, relayInfo.Billing.NeedsRefund())

	relayInfo.Billing.Refund(ctx)
	requireCreativeBillingSubscriptionUsed(t, 9411, 80)
	requireCreativeBillingTokenUnchanged(t, 9311, 777, 3)
	requireCreativeBillingTokenCount(t, 8311, 1)
}

func TestPostTextCreativeBillingMissingUsageSettlesZeroAndSkipsTokenQuota(t *testing.T) {
	cases := []struct {
		name  string
		usage *dto.Usage
	}{
		{name: "nil usage", usage: nil},
		{name: "empty usage payload", usage: &dto.Usage{}},
	}

	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			userId := 8320 + i
			tokenId := 9320 + i
			tokenKey := "creative-real-posttext-missing-usage-token-" + string(rune('a'+i))

			cleanupCreativeBillingTestRows(t)
			seedCreativeBillingUser(t, userId, 2000)
			seedCreativeBillingToken(t, tokenId, userId, tokenKey, 777, 3)

			ctx := newCreativeBillingTestContext()
			relayInfo := creativeBillingRelayInfo(userId, "creative-posttext-missing-usage-"+tc.name, "wallet_only")
			relayInfo.TokenId = tokenId
			relayInfo.TokenKey = tokenKey
			relayInfo.ChannelMeta = &relaycommon.ChannelMeta{}
			relayInfo.PriceData = types.PriceData{
				ModelRatio:      1,
				CompletionRatio: 1,
				GroupRatioInfo:  types.GroupRatioInfo{GroupRatio: 1},
			}
			relayInfo.SetEstimatePromptTokens(1000)

			apiErr := PreConsumeBilling(ctx, 120, relayInfo)
			require.Nil(t, apiErr)
			requireCreativeBillingUserQuota(t, userId, 1880)
			requireCreativeBillingTokenUnchanged(t, tokenId, 777, 3)

			PostTextConsumeQuota(ctx, relayInfo, tc.usage, nil)

			requireCreativeBillingUserQuota(t, userId, 2000)
			requireCreativeBillingTokenUnchanged(t, tokenId, 777, 3)
			requireCreativeBillingTokenCount(t, userId, 1)
			require.False(t, relayInfo.Billing.NeedsRefund())
		})
	}
}

func TestCreativeSessionBillingSubscriptionReserveRefundRestoresInitialAndExtraOnce(t *testing.T) {
	cleanupCreativeBillingTestRows(t)
	seedCreativeBillingUser(t, 8312, 0)
	seedCreativeBillingToken(t, 9312, 8312, "creative-real-subscription-reserve-token", 777, 3)
	seedCreativeBillingSubscription(t, 9412, 9512, 8312, 1000, 100)

	ctx := newCreativeBillingTestContext()
	relayInfo := creativeBillingRelayInfo(8312, "creative-subscription-reserve-refund", "subscription_only")

	apiErr := PreConsumeBilling(ctx, 120, relayInfo)
	require.Nil(t, apiErr)
	require.Equal(t, 120, relayInfo.Billing.GetPreConsumedQuota())
	requireCreativeBillingSubscriptionUsed(t, 9412, 220)
	requireCreativeBillingTokenUnchanged(t, 9312, 777, 3)

	require.NoError(t, relayInfo.Billing.Reserve(300))
	require.Equal(t, 300, relayInfo.Billing.GetPreConsumedQuota())
	require.Equal(t, 300, relayInfo.FinalPreConsumedQuota)
	require.Equal(t, int64(300), relayInfo.SubscriptionPreConsumed)
	requireCreativeBillingSubscriptionUsed(t, 9412, 400)
	requireCreativeBillingTokenUnchanged(t, 9312, 777, 3)

	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			relayInfo.Billing.Refund(ctx)
		}()
	}
	wg.Wait()

	require.Eventually(t, func() bool {
		var subscription model.UserSubscription
		err := model.DB.Where("id = ?", 9412).First(&subscription).Error
		return err == nil && subscription.AmountUsed == 100
	}, time.Second, 10*time.Millisecond)
	requireCreativeBillingSubscriptionUsed(t, 9412, 100)
	requireCreativeBillingTokenUnchanged(t, 9312, 777, 3)
	require.False(t, relayInfo.Billing.NeedsRefund())

	relayInfo.Billing.Refund(ctx)
	requireCreativeBillingSubscriptionUsed(t, 9412, 100)
	requireCreativeBillingTokenUnchanged(t, 9312, 777, 3)
}

func TestCreativeSessionBillingClientCancelAfterReserveRefundsInitialAndExtraOnceAndSkipsTokenQuota(t *testing.T) {
	cleanupCreativeBillingTestRows(t)
	seedCreativeBillingUser(t, 8314, 0)
	seedCreativeBillingToken(t, 9314, 8314, "creative-real-client-cancel-reserve-token", 777, 3)
	seedCreativeBillingSubscription(t, 9414, 9514, 8314, 1000, 100)

	ctx := newCreativeBillingTestContext()
	cancelCtx, cancel := context.WithCancel(ctx.Request.Context())
	ctx.Request = ctx.Request.WithContext(cancelCtx)
	relayInfo := creativeBillingRelayInfo(8314, "creative-subscription-client-cancel-reserve", "subscription_only")
	relayInfo.TokenId = 9314
	relayInfo.TokenKey = "creative-real-client-cancel-reserve-token"

	apiErr := PreConsumeBilling(ctx, 120, relayInfo)
	require.Nil(t, apiErr)
	require.Equal(t, 120, relayInfo.Billing.GetPreConsumedQuota())
	requireCreativeBillingSubscriptionUsed(t, 9414, 220)
	requireCreativeBillingTokenUnchanged(t, 9314, 777, 3)

	require.NoError(t, relayInfo.Billing.Reserve(300))
	require.Equal(t, 300, relayInfo.Billing.GetPreConsumedQuota())
	require.Equal(t, int64(300), relayInfo.SubscriptionPreConsumed)
	requireCreativeBillingSubscriptionUsed(t, 9414, 400)
	requireCreativeBillingTokenUnchanged(t, 9314, 777, 3)

	cancel()

	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Matches the controller defer failure path: any downstream/client-gone
			// error after reserve calls Billing.Refund on the existing session.
			relayInfo.Billing.Refund(ctx)
		}()
	}
	wg.Wait()

	require.Eventually(t, func() bool {
		var subscription model.UserSubscription
		err := model.DB.Where("id = ?", 9414).First(&subscription).Error
		return err == nil && subscription.AmountUsed == 100
	}, time.Second, 10*time.Millisecond)
	requireCreativeBillingSubscriptionUsed(t, 9414, 100)
	requireCreativeBillingTokenUnchanged(t, 9314, 777, 3)
	requireCreativeBillingTokenCount(t, 8314, 1)
	require.False(t, relayInfo.Billing.NeedsRefund())

	relayInfo.Billing.Refund(ctx)
	requireCreativeBillingSubscriptionUsed(t, 9414, 100)
	requireCreativeBillingTokenUnchanged(t, 9314, 777, 3)
}

func TestCreativeSessionBillingConcurrentRefundIsIdempotentAndSkipsTokenQuota(t *testing.T) {
	cleanupCreativeBillingTestRows(t)
	seedCreativeBillingUser(t, 8306, 500)
	seedCreativeBillingToken(t, 9306, 8306, "creative-real-concurrent-refund-token", 777, 3)

	ctx := newCreativeBillingTestContext()
	relayInfo := creativeBillingRelayInfo(8306, "creative-wallet-concurrent-refund", "wallet_only")

	apiErr := PreConsumeBilling(ctx, 120, relayInfo)
	require.Nil(t, apiErr)
	require.True(t, relayInfo.Billing.NeedsRefund())
	requireCreativeBillingUserQuota(t, 8306, 380)

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			relayInfo.Billing.Refund(ctx)
		}()
	}
	wg.Wait()

	require.Eventually(t, func() bool {
		quota, err := model.GetUserQuota(8306, true)
		return err == nil && quota == 500
	}, time.Second, 10*time.Millisecond)
	requireCreativeBillingTokenUnchanged(t, 9306, 777, 3)
	requireCreativeBillingTokenCount(t, 8306, 1)
	require.False(t, relayInfo.Billing.NeedsRefund())
}

func TestCreativeSessionBillingWalletFirstFallbackToSubscriptionSkipsTokenQuota(t *testing.T) {
	cleanupCreativeBillingTestRows(t)
	seedCreativeBillingUser(t, 8307, 10)
	seedCreativeBillingToken(t, 9307, 8307, "creative-real-fallback-token", 777, 3)
	seedCreativeBillingSubscription(t, 9407, 9507, 8307, 500, 0)

	ctx := newCreativeBillingTestContext()
	relayInfo := creativeBillingRelayInfo(8307, "creative-wallet-first-fallback", "wallet_first")

	apiErr := PreConsumeBilling(ctx, 50, relayInfo)
	require.Nil(t, apiErr)
	require.NotNil(t, relayInfo.Billing)
	require.Equal(t, BillingSourceSubscription, relayInfo.BillingSource)
	require.Equal(t, 9407, relayInfo.SubscriptionId)
	requireCreativeBillingUserQuota(t, 8307, 10)
	requireCreativeBillingSubscriptionUsed(t, 9407, 50)
	requireCreativeBillingTokenUnchanged(t, 9307, 777, 3)
	requireCreativeBillingTokenCount(t, 8307, 1)

	relayInfo.Billing.Refund(ctx)
	relayInfo.Billing.Refund(ctx)
	require.Eventually(t, func() bool {
		var subscription model.UserSubscription
		err := model.DB.Where("id = ?", 9407).First(&subscription).Error
		return err == nil && subscription.AmountUsed == 0
	}, time.Second, 10*time.Millisecond)
	requireCreativeBillingUserQuota(t, 8307, 10)
	requireCreativeBillingSubscriptionUsed(t, 9407, 0)
	requireCreativeBillingTokenUnchanged(t, 9307, 777, 3)
}

func TestCreativeSessionBillingSubscriptionFirstFallbackToWalletDoesNotDoubleChargeAndSettleIsIdempotent(t *testing.T) {
	cleanupCreativeBillingTestRows(t)
	seedCreativeBillingUser(t, 8313, 500)
	seedCreativeBillingToken(t, 9313, 8313, "creative-real-subscription-first-fallback-token", 777, 3)
	seedCreativeBillingSubscription(t, 9413, 9513, 8313, 100, 90)

	ctx := newCreativeBillingTestContext()
	relayInfo := creativeBillingRelayInfo(8313, "creative-subscription-first-fallback", "subscription_first")

	apiErr := PreConsumeBilling(ctx, 50, relayInfo)
	require.Nil(t, apiErr)
	require.NotNil(t, relayInfo.Billing)
	require.Equal(t, BillingSourceWallet, relayInfo.BillingSource)
	require.Equal(t, 50, relayInfo.FinalPreConsumedQuota)
	require.Equal(t, 0, relayInfo.SubscriptionId)
	requireCreativeBillingUserQuota(t, 8313, 450)
	requireCreativeBillingSubscriptionUsed(t, 9413, 90)
	requireCreativeBillingTokenUnchanged(t, 9313, 777, 3)
	requireCreativeBillingTokenCount(t, 8313, 1)

	require.NoError(t, SettleBilling(ctx, relayInfo, 50))
	require.NoError(t, SettleBilling(ctx, relayInfo, 50))
	requireCreativeBillingUserQuota(t, 8313, 450)
	requireCreativeBillingSubscriptionUsed(t, 9413, 90)
	requireCreativeBillingTokenUnchanged(t, 9313, 777, 3)
	require.False(t, relayInfo.Billing.NeedsRefund())

	relayInfo.Billing.Refund(ctx)
	requireCreativeBillingUserQuota(t, 8313, 450)
	requireCreativeBillingSubscriptionUsed(t, 9413, 90)
	requireCreativeBillingTokenUnchanged(t, 9313, 777, 3)
}

func cleanupCreativeBillingTestRows(t *testing.T) {
	t.Helper()
	creativeBillingMigrateOnce.Do(func() {
		creativeBillingMigrateErr = model.DB.AutoMigrate(
			&model.User{},
			&model.Token{},
			&model.SubscriptionPlan{},
			&model.UserSubscription{},
			&model.SubscriptionPreConsumeRecord{},
		)
	})
	require.NoError(t, creativeBillingMigrateErr)

	cleanup := func() {
		model.DB.Exec("DELETE FROM subscription_pre_consume_records")
		model.DB.Exec("DELETE FROM user_subscriptions")
		model.DB.Exec("DELETE FROM subscription_plans")
		model.DB.Exec("DELETE FROM tokens")
		model.DB.Exec("DELETE FROM users")
	}
	cleanup()
	t.Cleanup(cleanup)
}

func newCreativeBillingTestContext() *gin.Context {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest("POST", "/creative/relay/v1/chat/completions", nil)
	return ctx
}

func creativeBillingRelayInfo(userId int, requestId string, billingPreference string) *relaycommon.RelayInfo {
	return &relaycommon.RelayInfo{
		UserId:          userId,
		UserQuota:       0,
		OriginModelName: "creative-model-05",
		RequestId:       requestId,
		IsPlayground:    true,
		UsingGroup:      "default",
		UserGroup:       "default",
		RelayFormat:     types.RelayFormatOpenAI,
		UserSetting:     dto.UserSetting{BillingPreference: billingPreference},
	}
}

func seedCreativeBillingUser(t *testing.T, userId int, quota int) {
	t.Helper()
	require.NoError(t, model.DB.Create(&model.User{
		Id:       userId,
		Username: "creative-billing-user",
		Password: "password123",
		Status:   common.UserStatusEnabled,
		Quota:    quota,
		Group:    "default",
		AffCode:  "creative-billing-aff",
	}).Error)
}

func seedCreativeBillingToken(t *testing.T, tokenId int, userId int, key string, remainQuota int, usedQuota int) {
	t.Helper()
	require.NoError(t, model.DB.Create(&model.Token{
		Id:          tokenId,
		UserId:      userId,
		Key:         key,
		Name:        "real-api-token",
		Status:      common.TokenStatusEnabled,
		RemainQuota: remainQuota,
		UsedQuota:   usedQuota,
	}).Error)
}

func seedCreativeBillingSubscription(t *testing.T, subscriptionId int, planId int, userId int, total int64, used int64) {
	t.Helper()
	now := time.Now()
	require.NoError(t, model.DB.Create(&model.SubscriptionPlan{
		Id:            planId,
		Title:         "Creative Billing Plan",
		PriceAmount:   1,
		Currency:      "USD",
		DurationUnit:  model.SubscriptionDurationMonth,
		DurationValue: 1,
		Enabled:       true,
		TotalAmount:   total,
	}).Error)
	require.NoError(t, model.DB.Create(&model.UserSubscription{
		Id:          subscriptionId,
		UserId:      userId,
		PlanId:      planId,
		AmountTotal: total,
		AmountUsed:  used,
		StartTime:   now.Add(-time.Hour).Unix(),
		EndTime:     now.Add(30 * 24 * time.Hour).Unix(),
		Status:      "active",
		Source:      "admin",
	}).Error)
}

func requireCreativeBillingUserQuota(t *testing.T, userId int, want int) {
	t.Helper()
	quota, err := model.GetUserQuota(userId, true)
	require.NoError(t, err)
	require.Equal(t, want, quota)
}

func requireCreativeBillingSubscriptionUsed(t *testing.T, subscriptionId int, want int64) {
	t.Helper()
	var subscription model.UserSubscription
	require.NoError(t, model.DB.Where("id = ?", subscriptionId).First(&subscription).Error)
	require.Equal(t, want, subscription.AmountUsed)
}

func requireCreativeBillingTokenUnchanged(t *testing.T, tokenId int, wantRemain int, wantUsed int) {
	t.Helper()
	var token model.Token
	require.NoError(t, model.DB.Where("id = ?", tokenId).First(&token).Error)
	require.Equal(t, wantRemain, token.RemainQuota)
	require.Equal(t, wantUsed, token.UsedQuota)
}

func requireCreativeBillingTokenCount(t *testing.T, userId int, want int64) {
	t.Helper()
	var count int64
	require.NoError(t, model.DB.Model(&model.Token{}).Where("user_id = ?", userId).Count(&count).Error)
	require.Equal(t, want, count)
}
