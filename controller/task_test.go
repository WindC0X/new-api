package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestGetUserTaskRedactsCreativeImageChannelMetadata(t *testing.T) {
	setupCreativeControllerTestDB(t)
	require.NoError(t, model.DB.AutoMigrate(&model.Task{}))

	task := &model.Task{
		TaskID:    "task_creative_image_self_dto",
		Platform:  constant.TaskPlatformCreativeImage,
		UserId:    901,
		Group:     "default",
		ChannelId: 77,
		Status:    model.TaskStatusSuccess,
		Action:    creativeImageTaskActionGenerate,
	}
	task.SetData(map[string]any{
		"version":         1,
		"creativeManaged": true,
		"bindingId":       "mock:gpt-image-2:preview",
		"channelId":       77,
		"channel_id":      78,
		"userParams": map[string]any{
			"quality": "auto",
			"size":    "1024x1024",
		},
	})
	require.NoError(t, task.Insert())

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/task/self?platform=creative_image&p=1&page_size=10", nil)
	ctx.Set("id", 901)

	GetUserTask(ctx)

	require.Equal(t, http.StatusOK, recorder.Code)
	var response struct {
		Success bool `json:"success"`
		Data    struct {
			Total int `json:"total"`
			Items []struct {
				TaskID   string         `json:"task_id"`
				Platform string         `json:"platform"`
				Data     map[string]any `json:"data"`
			} `json:"items"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	require.True(t, response.Success)
	require.Equal(t, 1, response.Data.Total)
	require.Len(t, response.Data.Items, 1)
	item := response.Data.Items[0]
	require.Equal(t, "task_creative_image_self_dto", item.TaskID)
	require.Equal(t, string(constant.TaskPlatformCreativeImage), item.Platform)
	require.NotContains(t, item.Data, "channelId")
	require.NotContains(t, item.Data, "channel_id")
	require.Equal(t, true, item.Data["creativeManaged"])
	require.Equal(t, "mock:gpt-image-2:preview", item.Data["bindingId"])
	require.Equal(t, map[string]any{"quality": "auto", "size": "1024x1024"}, item.Data["userParams"])

	var stored model.Task
	require.NoError(t, model.DB.Where("task_id = ?", "task_creative_image_self_dto").First(&stored).Error)
	var storedData map[string]any
	require.NoError(t, stored.GetData(&storedData))
	require.Equal(t, 77, stored.ChannelId)
	require.Equal(t, float64(77), storedData["channelId"])
	require.Equal(t, float64(78), storedData["channel_id"])
}
