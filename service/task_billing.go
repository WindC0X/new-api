package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// LogTaskConsumption 记录任务消费日志和统计信息（仅记录，不涉及实际扣费）。
// 实际扣费已由 BillingSession（PreConsumeBilling + SettleBilling）完成。
func LogTaskConsumption(c *gin.Context, info *relaycommon.RelayInfo) {
	tokenName := c.GetString("token_name")
	logContent := fmt.Sprintf("操作 %s", info.Action)
	// 支持任务仅按次计费
	if common.StringsContains(constant.TaskPricePatches, info.OriginModelName) {
		logContent = fmt.Sprintf("%s，按次计费", logContent)
	} else {
		if len(info.PriceData.OtherRatios) > 0 {
			var contents []string
			for key, ra := range info.PriceData.OtherRatios {
				if 1.0 != ra {
					contents = append(contents, fmt.Sprintf("%s: %.2f", key, ra))
				}
			}
			if len(contents) > 0 {
				logContent = fmt.Sprintf("%s, 计算参数：%s", logContent, strings.Join(contents, ", "))
			}
		}
	}
	other := make(map[string]interface{})
	other["is_task"] = true
	other["request_path"] = c.Request.URL.Path
	other["model_price"] = info.PriceData.ModelPrice
	if info.PriceData.ModelRatio > 0 {
		other["model_ratio"] = info.PriceData.ModelRatio
	}
	other["group_ratio"] = info.PriceData.GroupRatioInfo.GroupRatio
	if info.PriceData.GroupRatioInfo.HasSpecialRatio {
		other["user_group_ratio"] = info.PriceData.GroupRatioInfo.GroupSpecialRatio
	}
	if info.IsModelMapped {
		other["is_model_mapped"] = true
		other["upstream_model_name"] = info.UpstreamModelName
	}
	model.RecordConsumeLog(c, info.UserId, model.RecordConsumeLogParams{
		ChannelId: info.ChannelId,
		ModelName: info.OriginModelName,
		TokenName: tokenName,
		Quota:     info.PriceData.Quota,
		Content:   logContent,
		TokenId:   info.TokenId,
		Group:     info.UsingGroup,
		Other:     other,
	})
	model.UpdateUserUsedQuotaAndRequestCount(info.UserId, info.PriceData.Quota)
	model.UpdateChannelUsedQuota(info.ChannelId, info.PriceData.Quota)
}

// ---------------------------------------------------------------------------
// 异步任务计费辅助函数
// ---------------------------------------------------------------------------

// resolveTokenKey 通过 TokenId 运行时获取令牌 Key（用于 Redis 缓存操作）。
// 如果令牌已被删除或查询失败，返回空字符串。
func resolveTokenKey(ctx context.Context, tokenId int, taskID string) string {
	token, err := model.GetTokenById(tokenId)
	if err != nil {
		logger.LogWarn(ctx, fmt.Sprintf("获取令牌 key 失败 (tokenId=%d, task=%s): %s", tokenId, taskID, err.Error()))
		return ""
	}
	return token.Key
}

// taskIsSubscription 判断任务是否通过订阅计费。
func taskIsSubscription(task *model.Task) bool {
	return task.PrivateData.BillingSource == BillingSourceSubscription && task.PrivateData.SubscriptionId > 0
}

// taskAdjustFunding 调整任务的资金来源（钱包或订阅），delta > 0 表示扣费，delta < 0 表示退还。
func taskAdjustFunding(task *model.Task, delta int) error {
	if taskIsSubscription(task) {
		return model.PostConsumeUserSubscriptionDeltaForUser(task.PrivateData.SubscriptionId, task.UserId, int64(delta))
	}
	if delta > 0 {
		return model.DecreaseUserQuota(task.UserId, delta, false)
	}
	return model.IncreaseUserQuota(task.UserId, -delta, false)
}

// taskAdjustTokenQuota 调整任务的令牌额度，delta > 0 表示扣费，delta < 0 表示退还。
// 需要通过 resolveTokenKey 运行时获取 key（不从 PrivateData 中读取）。
func taskAdjustTokenQuota(ctx context.Context, task *model.Task, delta int) error {
	if task.PrivateData.TokenId <= 0 || delta == 0 {
		return nil
	}
	tokenKey := resolveTokenKey(ctx, task.PrivateData.TokenId, task.TaskID)
	if tokenKey == "" {
		return nil
	}
	var err error
	if delta > 0 {
		err = model.DecreaseTokenQuota(task.PrivateData.TokenId, tokenKey, delta)
	} else {
		err = model.IncreaseTokenQuota(task.PrivateData.TokenId, tokenKey, -delta)
	}
	if err != nil {
		logger.LogWarn(ctx, fmt.Sprintf("调整令牌额度失败 (delta=%d, task=%s): %s", delta, task.TaskID, err.Error()))
	}
	return err
}

func taskAdjustFundingTx(tx *gorm.DB, task *model.Task, delta int) error {
	if delta == 0 {
		return nil
	}
	if taskIsSubscription(task) {
		return taskAdjustSubscriptionFundingTx(tx, task, delta)
	}
	if delta > 0 {
		return tx.Model(&model.User{}).Where("id = ?", task.UserId).Update("quota", gorm.Expr("quota - ?", delta)).Error
	}
	return tx.Model(&model.User{}).Where("id = ?", task.UserId).Update("quota", gorm.Expr("quota + ?", -delta)).Error
}

func taskAdjustSubscriptionFundingTx(tx *gorm.DB, task *model.Task, delta int) error {
	if task == nil || task.PrivateData.SubscriptionId <= 0 || task.UserId <= 0 {
		return fmt.Errorf("invalid subscription billing target")
	}
	return model.PostConsumeUserSubscriptionDeltaForUserTx(tx, task.PrivateData.SubscriptionId, task.UserId, int64(delta))
}

func taskAdjustTokenQuotaTx(tx *gorm.DB, task *model.Task, delta int) error {
	if task.PrivateData.TokenId <= 0 || delta == 0 {
		return nil
	}
	updates := map[string]interface{}{
		"accessed_time": common.GetTimestamp(),
	}
	if delta > 0 {
		updates["remain_quota"] = gorm.Expr("remain_quota - ?", delta)
		updates["used_quota"] = gorm.Expr("used_quota + ?", delta)
	} else {
		updates["remain_quota"] = gorm.Expr("remain_quota + ?", -delta)
		updates["used_quota"] = gorm.Expr("used_quota - ?", -delta)
	}
	return tx.Model(&model.Token{}).Where("id = ?", task.PrivateData.TokenId).Updates(updates).Error
}

func TaskSubmitPreConsumedQuota(relayInfo *relaycommon.RelayInfo) int {
	if relayInfo == nil {
		return 0
	}
	if relayInfo.Billing != nil {
		return relayInfo.Billing.GetPreConsumedQuota()
	}
	return relayInfo.FinalPreConsumedQuota
}

func SettleSubmittedTaskBillingDurably(ctx context.Context, task *model.Task, relayInfo *relaycommon.RelayInfo, actualQuota int) (*model.TaskBillingOutbox, error) {
	preConsumed := TaskSubmitPreConsumedQuota(relayInfo)
	if task != nil && task.PrivateData.BillingContext != nil && task.PrivateData.BillingContext.PreConsumedQuota == 0 {
		task.PrivateData.BillingContext.PreConsumedQuota = preConsumed
	}
	outbox, err := model.EnqueueTaskBillingOutbox(task, model.TaskBillingOutboxOperationSubmitSettle, actualQuota, preConsumed, "submit settle")
	if err != nil {
		return nil, err
	}
	if err := ProcessTaskBillingOutbox(ctx, outbox); err != nil {
		logger.LogWarn(ctx, fmt.Sprintf("提交任务 %s 结算已持久化待重试: %s", task.TaskID, err.Error()))
	}
	return outbox, nil
}

func ProcessPendingTaskBillingOutboxes(ctx context.Context, limit int) {
	if limit <= 0 {
		limit = 100
	}
	staleProcessingBefore := common.GetTimestamp() - 600
	var outboxes []model.TaskBillingOutbox
	if err := model.DB.Where("status IN ? OR (status = ? AND updated_time < ?)",
		[]string{model.TaskBillingOutboxStatusPending, model.TaskBillingOutboxStatusFailed},
		model.TaskBillingOutboxStatusProcessing,
		staleProcessingBefore).
		Order("id asc").
		Limit(limit).
		Find(&outboxes).Error; err != nil {
		logger.LogWarn(ctx, fmt.Sprintf("查询任务计费 outbox 失败: %s", err.Error()))
		return
	}
	for i := range outboxes {
		if err := ProcessTaskBillingOutbox(ctx, &outboxes[i]); err != nil {
			logger.LogWarn(ctx, fmt.Sprintf("处理任务计费 outbox #%d 失败: %s", outboxes[i].ID, err.Error()))
		}
	}
}

func ProcessTaskBillingOutbox(ctx context.Context, outbox *model.TaskBillingOutbox) error {
	if outbox == nil {
		return nil
	}
	claimed, err := claimTaskBillingOutbox(outbox)
	if err != nil {
		return err
	}
	if !claimed {
		return nil
	}
	var task model.Task
	if err := loadTaskForBillingOutbox(outbox, &task); err != nil {
		_ = markTaskBillingOutboxFailure(outbox.ID, err)
		return err
	}

	delta, nextQuota, shouldUpdateTaskQuota, shouldLog, logType, logQuota := taskBillingOutboxEffect(&task, outbox)
	err = model.DB.Transaction(func(tx *gorm.DB) error {
		var current model.TaskBillingOutbox
		if err := tx.Where("id = ?", outbox.ID).First(&current).Error; err != nil {
			return err
		}
		if current.Status == model.TaskBillingOutboxStatusDone {
			*outbox = current
			return nil
		}
		if current.Status != model.TaskBillingOutboxStatusProcessing {
			*outbox = current
			return nil
		}
		if !current.FundingDone {
			if err := taskAdjustFundingTx(tx, &task, delta); err != nil {
				return err
			}
			current.FundingDone = true
		}
		if !current.TokenDone {
			if err := taskAdjustTokenQuotaTx(tx, &task, delta); err != nil {
				return err
			}
			current.TokenDone = true
		}
		if shouldUpdateTaskQuota {
			if err := tx.Model(&model.Task{}).Where("id = ?", task.ID).Update("quota", nextQuota).Error; err != nil {
				return err
			}
			task.Quota = nextQuota
		}
		if !shouldLog {
			current.LogDone = true
		}
		current.LastError = ""
		if err := tx.Save(&current).Error; err != nil {
			return err
		}
		*outbox = current
		return nil
	})
	if err != nil {
		_ = markTaskBillingOutboxFailure(outbox.ID, err)
		return err
	}
	if outbox.Status == model.TaskBillingOutboxStatusDone {
		return nil
	}
	if shouldLog && !outbox.LogDone {
		recordTaskBillingOutboxLog(&task, outbox, logType, logQuota)
		if err := model.DB.Model(&model.TaskBillingOutbox{}).Where("id = ?", outbox.ID).Update("log_done", true).Error; err != nil {
			_ = markTaskBillingOutboxFailure(outbox.ID, err)
			return err
		}
		outbox.LogDone = true
	}
	if err := model.DB.Model(&model.TaskBillingOutbox{}).Where("id = ?", outbox.ID).Updates(map[string]interface{}{
		"status":         model.TaskBillingOutboxStatusDone,
		"completed_time": common.GetTimestamp(),
		"last_error":     "",
	}).Error; err != nil {
		_ = markTaskBillingOutboxFailure(outbox.ID, err)
		return err
	}
	outbox.Status = model.TaskBillingOutboxStatusDone
	outbox.CompletedTime = common.GetTimestamp()
	return nil
}

func claimTaskBillingOutbox(outbox *model.TaskBillingOutbox) (bool, error) {
	if outbox == nil || outbox.ID == 0 {
		return false, nil
	}
	staleProcessingBefore := common.GetTimestamp() - 600
	result := model.DB.Model(&model.TaskBillingOutbox{}).
		Where("id = ? AND (status IN ? OR (status = ? AND updated_time < ?))",
			outbox.ID,
			[]string{model.TaskBillingOutboxStatusPending, model.TaskBillingOutboxStatusFailed},
			model.TaskBillingOutboxStatusProcessing,
			staleProcessingBefore).
		Updates(map[string]interface{}{
			"status":     model.TaskBillingOutboxStatusProcessing,
			"attempts":   gorm.Expr("attempts + ?", 1),
			"last_error": "",
		})
	if result.Error != nil {
		return false, result.Error
	}
	if result.RowsAffected == 0 {
		var current model.TaskBillingOutbox
		if err := model.DB.Where("id = ?", outbox.ID).First(&current).Error; err != nil {
			return false, err
		}
		*outbox = current
		return false, nil
	}
	if err := model.DB.Where("id = ?", outbox.ID).First(outbox).Error; err != nil {
		return false, err
	}
	return true, nil
}

func loadTaskForBillingOutbox(outbox *model.TaskBillingOutbox, task *model.Task) error {
	if outbox == nil {
		return gorm.ErrRecordNotFound
	}
	if outbox.TaskRowID > 0 {
		return model.DB.Where("id = ? AND user_id = ? AND task_id = ?", outbox.TaskRowID, outbox.UserId, outbox.TaskID).First(task).Error
	}
	if outbox.UserId > 0 {
		return model.DB.Where("user_id = ? AND task_id = ?", outbox.UserId, outbox.TaskID).First(task).Error
	}
	return fmt.Errorf("task billing outbox owner unresolved: missing task_row_id and user_id for task_id=%s operation=%s", outbox.TaskID, outbox.Operation)
}

func taskBillingOutboxEffect(task *model.Task, outbox *model.TaskBillingOutbox) (delta int, nextQuota int, shouldUpdateTaskQuota bool, shouldLog bool, logType int, logQuota int) {
	switch outbox.Operation {
	case model.TaskBillingOutboxOperationSubmitSettle:
		delta = outbox.ActualQuota - outbox.PreConsumedQuota
		nextQuota = outbox.ActualQuota
		shouldUpdateTaskQuota = outbox.ActualQuota > 0 && task.Quota != outbox.ActualQuota
		return delta, nextQuota, shouldUpdateTaskQuota, false, 0, 0
	case model.TaskBillingOutboxOperationTerminalSettle:
		if outbox.ActualQuota <= 0 {
			return 0, task.Quota, false, false, 0, 0
		}
		delta = outbox.ActualQuota - task.Quota
		nextQuota = outbox.ActualQuota
		shouldUpdateTaskQuota = task.Quota != outbox.ActualQuota
		if delta > 0 {
			return delta, nextQuota, shouldUpdateTaskQuota, true, model.LogTypeConsume, delta
		}
		if delta < 0 {
			return delta, nextQuota, shouldUpdateTaskQuota, true, model.LogTypeRefund, -delta
		}
		return 0, nextQuota, shouldUpdateTaskQuota, false, 0, 0
	case model.TaskBillingOutboxOperationTerminalRefund:
		if task.Quota == 0 {
			return 0, task.Quota, false, false, 0, 0
		}
		return -task.Quota, task.Quota, false, true, model.LogTypeRefund, task.Quota
	default:
		return 0, task.Quota, false, false, 0, 0
	}
}

func recordTaskBillingOutboxLog(task *model.Task, outbox *model.TaskBillingOutbox, logType int, logQuota int) {
	if logQuota <= 0 {
		return
	}
	other := taskBillingOther(task)
	other["task_id"] = task.TaskID
	other["billing_outbox_id"] = outbox.ID
	if outbox.Reason != "" {
		other["reason"] = outbox.Reason
	}
	if outbox.Operation == model.TaskBillingOutboxOperationTerminalSettle {
		other["pre_consumed_quota"] = outbox.PreConsumedQuota
		other["actual_quota"] = outbox.ActualQuota
	}
	model.RecordTaskBillingLog(model.RecordTaskBillingLogParams{
		UserId:    task.UserId,
		LogType:   logType,
		Content:   outbox.Reason,
		ChannelId: task.ChannelId,
		ModelName: taskModelName(task),
		Quota:     logQuota,
		TokenId:   task.PrivateData.TokenId,
		Group:     task.Group,
		Other:     other,
	})
	if logType == model.LogTypeConsume {
		model.UpdateUserUsedQuotaAndRequestCount(task.UserId, logQuota)
		model.UpdateChannelUsedQuota(task.ChannelId, logQuota)
	}
}

func markTaskBillingOutboxFailure(outboxID int64, err error) error {
	if outboxID == 0 || err == nil {
		return nil
	}
	return model.DB.Model(&model.TaskBillingOutbox{}).
		Where("id = ?", outboxID).
		Updates(map[string]interface{}{
			"status":     model.TaskBillingOutboxStatusFailed,
			"last_error": err.Error(),
		}).Error
}

// taskBillingOther 从 task 的 BillingContext 构建日志 Other 字段。
func taskBillingOther(task *model.Task) map[string]interface{} {
	other := make(map[string]interface{})
	if bc := task.PrivateData.BillingContext; bc != nil {
		other["model_price"] = bc.ModelPrice
		if bc.ModelRatio > 0 {
			other["model_ratio"] = bc.ModelRatio
		}
		other["group_ratio"] = bc.GroupRatio
		if len(bc.OtherRatios) > 0 {
			for k, v := range bc.OtherRatios {
				other[k] = v
			}
		}
	}
	props := task.Properties
	if props.UpstreamModelName != "" && props.UpstreamModelName != props.OriginModelName {
		other["is_model_mapped"] = true
		other["upstream_model_name"] = props.UpstreamModelName
	}
	return other
}

// taskModelName 从 BillingContext 或 Properties 中获取模型名称。
func taskModelName(task *model.Task) string {
	if bc := task.PrivateData.BillingContext; bc != nil && bc.OriginModelName != "" {
		return bc.OriginModelName
	}
	return task.Properties.OriginModelName
}

// RefundTaskQuota 统一的任务失败退款逻辑。
// 当异步任务失败时，将预扣的 quota 退还给用户（支持钱包和订阅），并退还令牌额度。
func RefundTaskQuota(ctx context.Context, task *model.Task, reason string) error {
	quota := task.Quota
	if quota == 0 {
		return nil
	}

	// 1. 退还资金来源（钱包或订阅）
	if err := taskAdjustFunding(task, -quota); err != nil {
		logger.LogWarn(ctx, fmt.Sprintf("退还资金来源失败 task %s: %s", task.TaskID, err.Error()))
		return err
	}

	// 2. 退还令牌额度
	if err := taskAdjustTokenQuota(ctx, task, -quota); err != nil {
		return err
	}

	// 3. 记录日志
	other := taskBillingOther(task)
	other["task_id"] = task.TaskID
	other["reason"] = reason
	model.RecordTaskBillingLog(model.RecordTaskBillingLogParams{
		UserId:    task.UserId,
		LogType:   model.LogTypeRefund,
		Content:   "",
		ChannelId: task.ChannelId,
		ModelName: taskModelName(task),
		Quota:     quota,
		TokenId:   task.PrivateData.TokenId,
		Group:     task.Group,
		Other:     other,
	})
	return nil
}

// RecalculateTaskQuota 通用的异步差额结算。
// actualQuota 是任务完成后的实际应扣额度，与预扣额度 (task.Quota) 做差额结算。
// reason 用于日志记录（例如 "token重算" 或 "adaptor调整"）。
func RecalculateTaskQuota(ctx context.Context, task *model.Task, actualQuota int, reason string) error {
	if actualQuota <= 0 {
		return nil
	}
	preConsumedQuota := task.Quota
	quotaDelta := actualQuota - preConsumedQuota

	if quotaDelta == 0 {
		logger.LogInfo(ctx, fmt.Sprintf("任务 %s 预扣费准确（%s，%s）",
			task.TaskID, logger.LogQuota(actualQuota), reason))
		return nil
	}

	logger.LogInfo(ctx, fmt.Sprintf("任务 %s 差额结算：delta=%s（实际：%s，预扣：%s，%s）",
		task.TaskID,
		logger.LogQuota(quotaDelta),
		logger.LogQuota(actualQuota),
		logger.LogQuota(preConsumedQuota),
		reason,
	))

	// 调整资金来源
	if err := taskAdjustFunding(task, quotaDelta); err != nil {
		logger.LogError(ctx, fmt.Sprintf("差额结算资金调整失败 task %s: %s", task.TaskID, err.Error()))
		return err
	}

	// 调整令牌额度
	if err := taskAdjustTokenQuota(ctx, task, quotaDelta); err != nil {
		return err
	}

	task.Quota = actualQuota
	if task.ID != 0 {
		if err := model.DB.Model(&model.Task{}).Where("id = ?", task.ID).Update("quota", actualQuota).Error; err != nil {
			return err
		}
	}

	var logType int
	var logQuota int
	if quotaDelta > 0 {
		logType = model.LogTypeConsume
		logQuota = quotaDelta
		model.UpdateUserUsedQuotaAndRequestCount(task.UserId, quotaDelta)
		model.UpdateChannelUsedQuota(task.ChannelId, quotaDelta)
	} else {
		logType = model.LogTypeRefund
		logQuota = -quotaDelta
	}
	other := taskBillingOther(task)
	other["task_id"] = task.TaskID
	other["pre_consumed_quota"] = preConsumedQuota
	other["actual_quota"] = actualQuota
	model.RecordTaskBillingLog(model.RecordTaskBillingLogParams{
		UserId:    task.UserId,
		LogType:   logType,
		Content:   reason,
		ChannelId: task.ChannelId,
		ModelName: taskModelName(task),
		Quota:     logQuota,
		TokenId:   task.PrivateData.TokenId,
		Group:     task.Group,
		Other:     other,
	})
	return nil
}

// RecalculateTaskQuotaByTokens 根据实际 token 消耗重新计费（异步差额结算）。
// 当任务成功且返回了 totalTokens 时，根据模型倍率和分组倍率重新计算实际扣费额度，
// 与预扣费的差额进行补扣或退还。支持钱包和订阅计费来源。
func RecalculateTaskQuotaByTokens(ctx context.Context, task *model.Task, totalTokens int) error {
	actualQuota, ok := CalculateTaskQuotaByTokens(task, totalTokens)
	if !ok {
		return nil
	}
	reason := fmt.Sprintf("token重算：tokens=%d", totalTokens)
	return RecalculateTaskQuota(ctx, task, actualQuota, reason)
}

func CalculateTaskQuotaByTokens(task *model.Task, totalTokens int) (int, bool) {
	if totalTokens <= 0 {
		return 0, false
	}

	modelName := taskModelName(task)

	// 获取模型价格和倍率
	modelRatio, hasRatioSetting, _ := ratio_setting.GetModelRatio(modelName)
	// 只有配置了倍率(非固定价格)时才按 token 重新计费
	if !hasRatioSetting || modelRatio <= 0 {
		return 0, false
	}

	// 获取用户和组的倍率信息
	group := task.Group
	if group == "" {
		user, err := model.GetUserById(task.UserId, false)
		if err == nil {
			group = user.Group
		}
	}
	if group == "" {
		return 0, false
	}

	groupRatio := ratio_setting.GetGroupRatio(group)
	userGroupRatio, hasUserGroupRatio := ratio_setting.GetGroupGroupRatio(group, group)

	finalGroupRatio := groupRatio
	if hasUserGroupRatio {
		finalGroupRatio = userGroupRatio
	}

	// 计算 OtherRatios 乘积（视频折扣、时长等）
	otherMultiplier := 1.0
	if bc := task.PrivateData.BillingContext; bc != nil {
		for _, r := range bc.OtherRatios {
			if r != 1.0 && r > 0 {
				otherMultiplier *= r
			}
		}
	}

	// 计算实际应扣费额度: totalTokens * modelRatio * groupRatio * otherMultiplier
	return int(float64(totalTokens) * modelRatio * finalGroupRatio * otherMultiplier), true
}
