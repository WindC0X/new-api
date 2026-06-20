package model

import (
	"bytes"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	commonRelay "github.com/QuantumNous/new-api/relay/common"
	"gorm.io/gorm"
)

type TaskStatus string

func (t TaskStatus) ToVideoStatus() string {
	var status string
	switch t {
	case TaskStatusQueued, TaskStatusSubmitted:
		status = dto.VideoStatusQueued
	case TaskStatusInProgress:
		status = dto.VideoStatusInProgress
	case TaskStatusSuccess:
		status = dto.VideoStatusCompleted
	case TaskStatusFailure:
		status = dto.VideoStatusFailed
	default:
		status = dto.VideoStatusUnknown // Default fallback
	}
	return status
}

const (
	TaskStatusNotStart   TaskStatus = "NOT_START"
	TaskStatusSubmitted             = "SUBMITTED"
	TaskStatusQueued                = "QUEUED"
	TaskStatusInProgress            = "IN_PROGRESS"
	TaskStatusFailure               = "FAILURE"
	TaskStatusSuccess               = "SUCCESS"
	TaskStatusUnknown               = "UNKNOWN"
)

type Task struct {
	ID         int64                 `json:"id" gorm:"primary_key;AUTO_INCREMENT"`
	CreatedAt  int64                 `json:"created_at" gorm:"index"`
	UpdatedAt  int64                 `json:"updated_at"`
	TaskID     string                `json:"task_id" gorm:"type:varchar(191);index"` // 第三方id，不一定有/ song id\ Task id
	Platform   constant.TaskPlatform `json:"platform" gorm:"type:varchar(30);index"` // 平台
	UserId     int                   `json:"user_id" gorm:"index"`
	Group      string                `json:"group" gorm:"type:varchar(50)"` // 修正计费用
	ChannelId  int                   `json:"channel_id" gorm:"index"`
	Quota      int                   `json:"quota"`
	Action     string                `json:"action" gorm:"type:varchar(40);index"` // 任务类型, song, lyrics, description-mode
	Status     TaskStatus            `json:"status" gorm:"type:varchar(20);index"` // 任务状态
	FailReason string                `json:"fail_reason"`
	SubmitTime int64                 `json:"submit_time" gorm:"index"`
	StartTime  int64                 `json:"start_time" gorm:"index"`
	FinishTime int64                 `json:"finish_time" gorm:"index"`
	Progress   string                `json:"progress" gorm:"type:varchar(20);index"`
	Properties Properties            `json:"properties" gorm:"type:json"`
	Username   string                `json:"username,omitempty" gorm:"-"`
	// 禁止返回给用户，内部可能包含key等隐私信息
	PrivateData TaskPrivateData `json:"-" gorm:"column:private_data;type:json"`
	Data        json.RawMessage `json:"data" gorm:"type:json"`
}

func (t *Task) SetData(data any) {
	b, _ := common.Marshal(data)
	t.Data = json.RawMessage(b)
}

func (t *Task) GetData(v any) error {
	return common.Unmarshal(t.Data, &v)
}

type Properties struct {
	Input             string `json:"input"`
	UpstreamModelName string `json:"upstream_model_name,omitempty"`
	OriginModelName   string `json:"origin_model_name,omitempty"`
}

func (m *Properties) Scan(val interface{}) error {
	bytesValue, _ := val.([]byte)
	if len(bytesValue) == 0 {
		*m = Properties{}
		return nil
	}
	return common.Unmarshal(bytesValue, m)
}

func (m Properties) Value() (driver.Value, error) {
	if m == (Properties{}) {
		return nil, nil
	}
	return common.Marshal(m)
}

type TaskPrivateData struct {
	Key              string `json:"key,omitempty"`
	UpstreamTaskID   string `json:"upstream_task_id,omitempty"`  // 上游真实 task ID
	ProviderEndpoint string `json:"provider_endpoint,omitempty"` // 提交时的 provider endpoint 快照，用于轮询亲和性校验
	IdempotencyKey   string `json:"idempotency_key,omitempty"`
	ResultURL        string `json:"result_url,omitempty"` // 任务成功后的结果 URL（视频地址等）
	// 计费上下文：用于异步退款/差额结算（轮询阶段读取）
	BillingSource  string              `json:"billing_source,omitempty"`  // "wallet" 或 "subscription"
	SubscriptionId int                 `json:"subscription_id,omitempty"` // 订阅 ID，用于订阅退款
	TokenId        int                 `json:"token_id,omitempty"`        // 令牌 ID，用于令牌额度退款
	BillingContext *TaskBillingContext `json:"billing_context,omitempty"` // 计费参数快照（用于轮询阶段重新计算）
}

// TaskBillingContext 记录任务提交时的计费参数，以便轮询阶段可以重新计算额度。
type TaskBillingContext struct {
	ModelPrice       float64            `json:"model_price,omitempty"`        // 模型单价
	GroupRatio       float64            `json:"group_ratio,omitempty"`        // 分组倍率
	ModelRatio       float64            `json:"model_ratio,omitempty"`        // 模型倍率
	OtherRatios      map[string]float64 `json:"other_ratios,omitempty"`       // 附加倍率（时长、分辨率等）
	OriginModelName  string             `json:"origin_model_name,omitempty"`  // 模型名称，必须为OriginModelName
	PerCallBilling   bool               `json:"per_call_billing,omitempty"`   // 按次计费：跳过轮询阶段的差额结算
	PreConsumedQuota int                `json:"pre_consumed_quota,omitempty"` // 提交阶段预扣额度快照，用于 durable settle 重试
}

// GetUpstreamTaskID 获取上游真实 task ID（用于与 provider 通信）
// 旧数据没有 UpstreamTaskID 时，TaskID 本身就是上游 ID
func (t *Task) GetUpstreamTaskID() string {
	if t.PrivateData.UpstreamTaskID != "" {
		return t.PrivateData.UpstreamTaskID
	}
	return t.TaskID
}

// GetResultURL 获取任务结果 URL（视频地址等）
// 新数据存在 PrivateData.ResultURL 中；旧数据回退到 FailReason（历史兼容）
func (t *Task) GetResultURL() string {
	if t.PrivateData.ResultURL != "" {
		return t.PrivateData.ResultURL
	}
	return t.FailReason
}

// GenerateTaskID 生成对外暴露的 task_xxxx 格式 ID
func GenerateTaskID() string {
	key, _ := common.GenerateRandomCharsKey(32)
	return "task_" + key
}

func (p *TaskPrivateData) Scan(val interface{}) error {
	bytesValue, _ := val.([]byte)
	if len(bytesValue) == 0 {
		return nil
	}
	return common.Unmarshal(bytesValue, p)
}

func (p TaskPrivateData) Value() (driver.Value, error) {
	if (p == TaskPrivateData{}) {
		return nil, nil
	}
	return common.Marshal(p)
}

type CreativeVideoIdempotency struct {
	ID          int64  `json:"id" gorm:"primary_key;AUTO_INCREMENT"`
	UserId      int    `json:"user_id" gorm:"uniqueIndex:idx_creative_video_idempotency_user_scope_request,priority:1;index"`
	Scope       string `json:"scope" gorm:"type:varchar(64);uniqueIndex:idx_creative_video_idempotency_user_scope_request,priority:2;index"`
	RequestID   string `json:"request_id" gorm:"type:varchar(128);uniqueIndex:idx_creative_video_idempotency_user_scope_request,priority:3"`
	PayloadHash string `json:"payload_hash" gorm:"type:varchar(64);index"`
	TaskID      string `json:"task_id" gorm:"type:varchar(191);index"`
	CreatedTime int64  `json:"created_time" gorm:"bigint"`
	UpdatedTime int64  `json:"updated_time" gorm:"bigint;index"`
}

func (r *CreativeVideoIdempotency) BeforeCreate(tx *gorm.DB) error {
	now := common.GetTimestamp()
	r.CreatedTime = now
	r.UpdatedTime = now
	return nil
}

func (r *CreativeVideoIdempotency) BeforeUpdate(tx *gorm.DB) error {
	r.UpdatedTime = common.GetTimestamp()
	return nil
}

const CreativeVideoIdempotencyScopeVideoSubmit = "video.submit"

func normalizeCreativeVideoIdempotencyScope(scope string) string {
	scope = strings.TrimSpace(scope)
	if scope == "" {
		return CreativeVideoIdempotencyScopeVideoSubmit
	}
	return scope
}

func PrepareCreativeVideoIdempotency(userID int, requestID string, payloadHash string) (*CreativeVideoIdempotency, bool, error) {
	return PrepareCreativeVideoIdempotencyScoped(userID, CreativeVideoIdempotencyScopeVideoSubmit, requestID, payloadHash)
}

func PrepareCreativeVideoIdempotencyScoped(userID int, scope string, requestID string, payloadHash string) (*CreativeVideoIdempotency, bool, error) {
	scope = normalizeCreativeVideoIdempotencyScope(scope)
	requestID = strings.TrimSpace(requestID)
	payloadHash = strings.TrimSpace(payloadHash)
	if userID <= 0 || scope == "" || requestID == "" || payloadHash == "" {
		return nil, false, errors.New("invalid creative video idempotency args")
	}
	var existing CreativeVideoIdempotency
	err := DB.Where("user_id = ? AND scope = ? AND request_id = ?", userID, scope, requestID).First(&existing).Error
	if errors.Is(err, gorm.ErrRecordNotFound) && scope == CreativeVideoIdempotencyScopeVideoSubmit {
		err = DB.Where("user_id = ? AND scope = ? AND request_id = ?", userID, "", requestID).First(&existing).Error
	}
	if err == nil {
		return &existing, true, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, err
	}
	record := &CreativeVideoIdempotency{
		UserId:      userID,
		Scope:       scope,
		RequestID:   requestID,
		PayloadHash: payloadHash,
		TaskID:      GenerateTaskID(),
	}
	if err := DB.Create(record).Error; err != nil {
		var dup CreativeVideoIdempotency
		if dupErr := DB.Where("user_id = ? AND scope = ? AND request_id = ?", userID, scope, requestID).First(&dup).Error; dupErr == nil {
			return &dup, true, nil
		}
		return nil, false, err
	}
	return record, false, nil
}

func CompleteCreativeVideoIdempotency(userID int, requestID string, taskID string) error {
	return CompleteCreativeVideoIdempotencyScoped(userID, CreativeVideoIdempotencyScopeVideoSubmit, requestID, taskID)
}

func CompleteCreativeVideoIdempotencyScoped(userID int, scope string, requestID string, taskID string) error {
	scope = normalizeCreativeVideoIdempotencyScope(scope)
	requestID = strings.TrimSpace(requestID)
	taskID = strings.TrimSpace(taskID)
	if userID <= 0 || requestID == "" || taskID == "" {
		return nil
	}
	return DB.Model(&CreativeVideoIdempotency{}).
		Where("user_id = ? AND scope = ? AND request_id = ?", userID, scope, requestID).
		Update("task_id", taskID).Error
}

func DeleteCreativeVideoIdempotency(userID int, requestID string) error {
	return DeleteCreativeVideoIdempotencyScoped(userID, CreativeVideoIdempotencyScopeVideoSubmit, requestID)
}

func DeleteCreativeVideoIdempotencyScoped(userID int, scope string, requestID string) error {
	scope = normalizeCreativeVideoIdempotencyScope(scope)
	requestID = strings.TrimSpace(requestID)
	if userID <= 0 || requestID == "" {
		return nil
	}
	return DB.Where("user_id = ? AND scope = ? AND request_id = ?", userID, scope, requestID).Delete(&CreativeVideoIdempotency{}).Error
}

// SyncTaskQueryParams 用于包含所有搜索条件的结构体，可以根据需求添加更多字段
type SyncTaskQueryParams struct {
	Platform       constant.TaskPlatform
	ChannelID      string
	TaskID         string
	UserID         string
	Action         string
	Status         string
	StartTimestamp int64
	EndTimestamp   int64
	UserIDs        []int
}

func InitTask(platform constant.TaskPlatform, relayInfo *commonRelay.RelayInfo) *Task {
	properties := Properties{}
	privateData := TaskPrivateData{}
	if relayInfo != nil && relayInfo.ChannelMeta != nil {
		if relayInfo.ChannelMeta.ApiKey != "" {
			privateData.Key = relayInfo.ChannelMeta.ApiKey
		}
		if relayInfo.TaskRelayInfo != nil && relayInfo.TaskRelayInfo.IdempotencyKey != "" {
			privateData.IdempotencyKey = relayInfo.TaskRelayInfo.IdempotencyKey
		}
		if relayInfo.UpstreamModelName != "" {
			properties.UpstreamModelName = relayInfo.UpstreamModelName
		}
		if relayInfo.OriginModelName != "" {
			properties.OriginModelName = relayInfo.OriginModelName
		}
	}

	// 使用预生成的公开 ID（如果有），否则新生成
	taskID := ""
	if relayInfo.TaskRelayInfo != nil && relayInfo.TaskRelayInfo.PublicTaskID != "" {
		taskID = relayInfo.TaskRelayInfo.PublicTaskID
	} else {
		taskID = GenerateTaskID()
	}

	t := &Task{
		TaskID:      taskID,
		UserId:      relayInfo.UserId,
		Group:       relayInfo.UsingGroup,
		SubmitTime:  time.Now().Unix(),
		Status:      TaskStatusNotStart,
		Progress:    "0%",
		ChannelId:   relayInfo.ChannelId,
		Platform:    platform,
		Properties:  properties,
		PrivateData: privateData,
	}
	return t
}

func TaskGetAllUserTask(userId int, startIdx int, num int, queryParams SyncTaskQueryParams) []*Task {
	var tasks []*Task
	var err error

	// 初始化查询构建器
	query := DB.Where("user_id = ?", userId)

	if queryParams.TaskID != "" {
		query = query.Where("task_id = ?", queryParams.TaskID)
	}
	if queryParams.Action != "" {
		query = query.Where("action = ?", queryParams.Action)
	}
	if queryParams.Status != "" {
		query = query.Where("status = ?", queryParams.Status)
	}
	if queryParams.Platform != "" {
		query = query.Where("platform = ?", queryParams.Platform)
	}
	if queryParams.StartTimestamp != 0 {
		// 假设您已将前端传来的时间戳转换为数据库所需的时间格式，并处理了时间戳的验证和解析
		query = query.Where("submit_time >= ?", queryParams.StartTimestamp)
	}
	if queryParams.EndTimestamp != 0 {
		query = query.Where("submit_time <= ?", queryParams.EndTimestamp)
	}

	// 获取数据
	err = query.Omit("channel_id").Order("id desc").Limit(num).Offset(startIdx).Find(&tasks).Error
	if err != nil {
		return nil
	}

	return tasks
}

func TaskGetAllTasks(startIdx int, num int, queryParams SyncTaskQueryParams) []*Task {
	var tasks []*Task
	var err error

	// 初始化查询构建器
	query := DB

	// 添加过滤条件
	if queryParams.ChannelID != "" {
		query = query.Where("channel_id = ?", queryParams.ChannelID)
	}
	if queryParams.Platform != "" {
		query = query.Where("platform = ?", queryParams.Platform)
	}
	if queryParams.UserID != "" {
		query = query.Where("user_id = ?", queryParams.UserID)
	}
	if len(queryParams.UserIDs) != 0 {
		query = query.Where("user_id in (?)", queryParams.UserIDs)
	}
	if queryParams.TaskID != "" {
		query = query.Where("task_id = ?", queryParams.TaskID)
	}
	if queryParams.Action != "" {
		query = query.Where("action = ?", queryParams.Action)
	}
	if queryParams.Status != "" {
		query = query.Where("status = ?", queryParams.Status)
	}
	if queryParams.StartTimestamp != 0 {
		query = query.Where("submit_time >= ?", queryParams.StartTimestamp)
	}
	if queryParams.EndTimestamp != 0 {
		query = query.Where("submit_time <= ?", queryParams.EndTimestamp)
	}

	// 获取数据
	err = query.Order("id desc").Limit(num).Offset(startIdx).Find(&tasks).Error
	if err != nil {
		return nil
	}

	return tasks
}

func GetTimedOutUnfinishedTasks(cutoffUnix int64, limit int) []*Task {
	var tasks []*Task
	err := DB.Where("progress != ?", "100%").
		Where("status NOT IN ?", []string{TaskStatusFailure, TaskStatusSuccess}).
		Where("submit_time < ?", cutoffUnix).
		Order("submit_time").
		Limit(limit).
		Find(&tasks).Error
	if err != nil {
		return nil
	}
	return tasks
}

func GetAllUnFinishSyncTasks(limit int) []*Task {
	var tasks []*Task
	var err error
	// get all tasks progress is not 100%
	err = DB.Where("progress != ?", "100%").Where("status != ?", TaskStatusFailure).Where("status != ?", TaskStatusSuccess).Limit(limit).Order("id").Find(&tasks).Error
	if err != nil {
		return nil
	}
	return tasks
}

func GetByOnlyTaskId(taskId string) (*Task, bool, error) {
	if taskId == "" {
		return nil, false, nil
	}
	var task *Task
	var err error
	err = DB.Where("task_id = ?", taskId).First(&task).Error
	exist, err := RecordExist(err)
	if err != nil {
		return nil, false, err
	}
	return task, exist, err
}

func GetByTaskId(userId int, taskId string) (*Task, bool, error) {
	if taskId == "" {
		return nil, false, nil
	}
	var task *Task
	var err error
	err = DB.Where("user_id = ? and task_id = ?", userId, taskId).
		First(&task).Error
	exist, err := RecordExist(err)
	if err != nil {
		return nil, false, err
	}
	return task, exist, err
}

func GetByTaskIds(userId int, taskIds []any) ([]*Task, error) {
	if len(taskIds) == 0 {
		return nil, nil
	}
	var task []*Task
	var err error
	err = DB.Where("user_id = ? and task_id in (?)", userId, taskIds).
		Find(&task).Error
	if err != nil {
		return nil, err
	}
	return task, nil
}

func (Task *Task) Insert() error {
	var err error
	err = DB.Create(Task).Error
	return err
}

type taskSnapshot struct {
	Status     TaskStatus
	Progress   string
	StartTime  int64
	FinishTime int64
	FailReason string
	ResultURL  string
	Data       json.RawMessage
}

func (s taskSnapshot) Equal(other taskSnapshot) bool {
	return s.Status == other.Status &&
		s.Progress == other.Progress &&
		s.StartTime == other.StartTime &&
		s.FinishTime == other.FinishTime &&
		s.FailReason == other.FailReason &&
		s.ResultURL == other.ResultURL &&
		bytes.Equal(s.Data, other.Data)
}

func (t *Task) Snapshot() taskSnapshot {
	return taskSnapshot{
		Status:     t.Status,
		Progress:   t.Progress,
		StartTime:  t.StartTime,
		FinishTime: t.FinishTime,
		FailReason: t.FailReason,
		ResultURL:  t.PrivateData.ResultURL,
		Data:       t.Data,
	}
}

func (Task *Task) Update() error {
	var err error
	err = DB.Save(Task).Error
	return err
}

// UpdateWithStatus performs a conditional UPDATE guarded by fromStatus (CAS).
// Returns (true, nil) if this caller won the update, (false, nil) if
// another process already moved the task out of fromStatus.
//
// Uses Model().Select("*").Updates() instead of Save() because GORM's Save
// falls back to INSERT ON CONFLICT when the WHERE-guarded UPDATE matches
// zero rows, which silently bypasses the CAS guard.
func (t *Task) UpdateWithStatus(fromStatus TaskStatus) (bool, error) {
	result := DB.Model(t).Where("status = ?", fromStatus).Select("*").Updates(t)
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

const (
	TaskBillingOutboxOperationSubmitSettle   = "submit_settle"
	TaskBillingOutboxOperationTerminalSettle = "terminal_settle"
	TaskBillingOutboxOperationTerminalRefund = "terminal_refund"

	TaskBillingOutboxStatusPending    = "pending"
	TaskBillingOutboxStatusProcessing = "processing"
	TaskBillingOutboxStatusDone       = "done"
	TaskBillingOutboxStatusFailed     = "failed"
)

type TaskBillingOutbox struct {
	ID               int64  `json:"id" gorm:"primary_key;AUTO_INCREMENT"`
	TaskRowID        int64  `json:"task_row_id" gorm:"index;uniqueIndex:idx_task_billing_outbox_owner_task_op,priority:1"`
	TaskID           string `json:"task_id" gorm:"type:varchar(191);index;uniqueIndex:idx_task_billing_outbox_owner_task_op,priority:3"`
	UserId           int    `json:"user_id" gorm:"index;uniqueIndex:idx_task_billing_outbox_owner_task_op,priority:2"`
	Operation        string `json:"operation" gorm:"type:varchar(40);index;uniqueIndex:idx_task_billing_outbox_owner_task_op,priority:4"`
	Status           string `json:"status" gorm:"type:varchar(20);index"`
	ActualQuota      int    `json:"actual_quota"`
	PreConsumedQuota int    `json:"pre_consumed_quota"`
	Reason           string `json:"reason" gorm:"type:text"`
	FundingDone      bool   `json:"funding_done" gorm:"index"`
	TokenDone        bool   `json:"token_done" gorm:"index"`
	LogDone          bool   `json:"log_done" gorm:"index"`
	Attempts         int    `json:"attempts"`
	LastError        string `json:"last_error" gorm:"type:text"`
	CreatedTime      int64  `json:"created_time" gorm:"bigint"`
	UpdatedTime      int64  `json:"updated_time" gorm:"bigint;index"`
	CompletedTime    int64  `json:"completed_time" gorm:"bigint;index"`
}

func (r *TaskBillingOutbox) BeforeCreate(tx *gorm.DB) error {
	now := common.GetTimestamp()
	if r.Status == "" {
		r.Status = TaskBillingOutboxStatusPending
	}
	r.CreatedTime = now
	r.UpdatedTime = now
	return nil
}

func (r *TaskBillingOutbox) BeforeUpdate(tx *gorm.DB) error {
	r.UpdatedTime = common.GetTimestamp()
	return nil
}

func EnqueueTaskBillingOutbox(task *Task, operation string, actualQuota int, preConsumedQuota int, reason string) (*TaskBillingOutbox, error) {
	return enqueueTaskBillingOutbox(DB, task, operation, actualQuota, preConsumedQuota, reason)
}

func EnqueueTaskBillingOutboxTx(tx *gorm.DB, task *Task, operation string, actualQuota int, preConsumedQuota int, reason string) (*TaskBillingOutbox, error) {
	return enqueueTaskBillingOutbox(tx, task, operation, actualQuota, preConsumedQuota, reason)
}

func enqueueTaskBillingOutbox(tx *gorm.DB, task *Task, operation string, actualQuota int, preConsumedQuota int, reason string) (*TaskBillingOutbox, error) {
	if task == nil {
		return nil, errors.New("task is nil")
	}
	operation = strings.TrimSpace(operation)
	if operation == "" {
		return nil, errors.New("task billing outbox operation is empty")
	}
	record := &TaskBillingOutbox{
		TaskRowID:        task.ID,
		TaskID:           task.TaskID,
		UserId:           task.UserId,
		Operation:        operation,
		Status:           TaskBillingOutboxStatusPending,
		ActualQuota:      actualQuota,
		PreConsumedQuota: preConsumedQuota,
		Reason:           reason,
	}
	err := tx.Where("task_row_id = ? AND user_id = ? AND task_id = ? AND operation = ?", task.ID, task.UserId, task.TaskID, operation).
		Attrs(record).
		FirstOrCreate(record).Error
	if err != nil {
		return nil, err
	}
	return record, nil
}

func (t *Task) UpdateWithStatusAndBillingOutbox(fromStatus TaskStatus, operation string, actualQuota int, preConsumedQuota int, reason string) (bool, *TaskBillingOutbox, error) {
	var outbox *TaskBillingOutbox
	won := false
	err := DB.Transaction(func(tx *gorm.DB) error {
		result := tx.Model(t).Where("status = ?", fromStatus).Select("*").Updates(t)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return nil
		}
		won = true
		var err error
		outbox, err = enqueueTaskBillingOutbox(tx, t, operation, actualQuota, preConsumedQuota, reason)
		return err
	})
	return won, outbox, err
}

// TaskBulkUpdate performs an unconditional bulk UPDATE by upstream task_id strings.
// Same caveats as TaskBulkUpdateByID — no CAS guard.
func TaskBulkUpdate(taskIds []string, params map[string]any) error {
	if len(taskIds) == 0 {
		return nil
	}
	return DB.Model(&Task{}).
		Where("task_id in (?)", taskIds).
		Updates(params).Error
}

// TaskBulkUpdateByID performs an unconditional bulk UPDATE by primary key IDs.
// WARNING: This function has NO CAS (Compare-And-Swap) guard — it will overwrite
// any concurrent status changes. DO NOT use in billing/quota lifecycle flows
// (e.g., timeout, success, failure transitions that trigger refunds or settlements).
// For status transitions that involve billing, use Task.UpdateWithStatus() instead.
func TaskBulkUpdateByID(ids []int64, params map[string]any) error {
	if len(ids) == 0 {
		return nil
	}
	return DB.Model(&Task{}).
		Where("id in (?)", ids).
		Updates(params).Error
}

type TaskQuotaUsage struct {
	Mode  string  `json:"mode"`
	Count float64 `json:"count"`
}

// TaskCountAllTasks returns total tasks that match the given query params (admin usage)
func TaskCountAllTasks(queryParams SyncTaskQueryParams) int64 {
	var total int64
	query := DB.Model(&Task{})
	if queryParams.ChannelID != "" {
		query = query.Where("channel_id = ?", queryParams.ChannelID)
	}
	if queryParams.Platform != "" {
		query = query.Where("platform = ?", queryParams.Platform)
	}
	if queryParams.UserID != "" {
		query = query.Where("user_id = ?", queryParams.UserID)
	}
	if len(queryParams.UserIDs) != 0 {
		query = query.Where("user_id in (?)", queryParams.UserIDs)
	}
	if queryParams.TaskID != "" {
		query = query.Where("task_id = ?", queryParams.TaskID)
	}
	if queryParams.Action != "" {
		query = query.Where("action = ?", queryParams.Action)
	}
	if queryParams.Status != "" {
		query = query.Where("status = ?", queryParams.Status)
	}
	if queryParams.StartTimestamp != 0 {
		query = query.Where("submit_time >= ?", queryParams.StartTimestamp)
	}
	if queryParams.EndTimestamp != 0 {
		query = query.Where("submit_time <= ?", queryParams.EndTimestamp)
	}
	_ = query.Count(&total).Error
	return total
}

// TaskCountAllUserTask returns total tasks for given user
func TaskCountAllUserTask(userId int, queryParams SyncTaskQueryParams) int64 {
	var total int64
	query := DB.Model(&Task{}).Where("user_id = ?", userId)
	if queryParams.TaskID != "" {
		query = query.Where("task_id = ?", queryParams.TaskID)
	}
	if queryParams.Action != "" {
		query = query.Where("action = ?", queryParams.Action)
	}
	if queryParams.Status != "" {
		query = query.Where("status = ?", queryParams.Status)
	}
	if queryParams.Platform != "" {
		query = query.Where("platform = ?", queryParams.Platform)
	}
	if queryParams.StartTimestamp != 0 {
		query = query.Where("submit_time >= ?", queryParams.StartTimestamp)
	}
	if queryParams.EndTimestamp != 0 {
		query = query.Where("submit_time <= ?", queryParams.EndTimestamp)
	}
	_ = query.Count(&total).Error
	return total
}
func (t *Task) ToOpenAIVideo() *dto.OpenAIVideo {
	openAIVideo := dto.NewOpenAIVideo()
	openAIVideo.ID = t.TaskID
	openAIVideo.Status = t.Status.ToVideoStatus()
	openAIVideo.Model = t.Properties.OriginModelName
	openAIVideo.SetProgressStr(t.Progress)
	openAIVideo.CreatedAt = t.CreatedAt
	openAIVideo.CompletedAt = t.UpdatedAt
	openAIVideo.SetMetadata("url", t.GetResultURL())
	return openAIVideo
}
