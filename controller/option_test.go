package controller

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestUpdateOptionRejectsCreativeModelBindingsGenericWrite(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.PUT("/api/option/", UpdateOption)

	body := []byte(`{"key":"` + service.CreativeModelBindingsOptionKey + `","value":"{\"version\":1,\"bindings\":[]}"}`)
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/option/", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, req)

	require.Equal(t, http.StatusOK, recorder.Code)
	var payload map[string]any
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &payload))
	require.Equal(t, false, payload["success"])
	require.Contains(t, payload["message"], "/api/creative/model-bindings")
}
