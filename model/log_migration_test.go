package model

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type legacyLogWithoutTaskBillingOutboxID struct {
	Id        int   `gorm:"primary_key;AUTO_INCREMENT"`
	UserId    int   `gorm:"index"`
	CreatedAt int64 `gorm:"bigint"`
	Type      int   `gorm:"index"`
	Content   string
}

func (legacyLogWithoutTaskBillingOutboxID) TableName() string {
	return "logs"
}

func TestEnsureSQLiteLogTaskBillingOutboxColumnAllowsAutoMigrateExistingLogs(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)

	require.NoError(t, db.AutoMigrate(&legacyLogWithoutTaskBillingOutboxID{}))
	require.NoError(t, db.Create(&legacyLogWithoutTaskBillingOutboxID{
		UserId:    1,
		CreatedAt: 1,
		Type:      LogTypeConsume,
		Content:   "legacy",
	}).Error)

	require.NoError(t, ensureSQLiteLogTaskBillingOutboxColumn(db))
	require.NoError(t, db.AutoMigrate(&Log{}))
	require.NoError(t, ensureLogTaskBillingOutboxIndex(db))
	require.True(t, db.Migrator().HasColumn(&Log{}, "TaskBillingOutboxID"))
	require.True(t, db.Migrator().HasIndex(&Log{}, "idx_logs_task_billing_outbox_id"))

	outboxID := int64(1001)
	require.NoError(t, db.Create(&Log{
		UserId:              1,
		CreatedAt:           2,
		Type:                LogTypeConsume,
		Content:             "first",
		TaskBillingOutboxID: &outboxID,
	}).Error)
	err = db.Create(&Log{
		UserId:              1,
		CreatedAt:           3,
		Type:                LogTypeConsume,
		Content:             "duplicate",
		TaskBillingOutboxID: &outboxID,
	}).Error
	require.Error(t, err)
}
