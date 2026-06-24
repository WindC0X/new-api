package model

import (
	"errors"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestChannelNotFoundErrorClassification(t *testing.T) {
	require.True(t, IsChannelNotFoundError(gorm.ErrRecordNotFound))
	require.True(t, IsChannelNotFoundError(errors.Join(errors.New("lookup"), gorm.ErrRecordNotFound)))
	require.False(t, IsChannelNotFoundError(errors.New("temporary database unavailable")))
}

func TestCacheGetChannelMissingMemoryCacheIsClassifiedNotFound(t *testing.T) {
	previous := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = true
	t.Cleanup(func() { common.MemoryCacheEnabled = previous })

	channelsIDM = map[int]*Channel{}
	t.Cleanup(func() { channelsIDM = nil })

	channel, err := CacheGetChannel(987654)
	require.Nil(t, channel)
	require.Error(t, err)
	require.True(t, IsChannelNotFoundError(err))
}
