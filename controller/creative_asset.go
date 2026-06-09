package controller

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

const (
	creativeAssetMultipartPartLimit  = 8
	creativeAssetMultipartFieldLimit = 4096
)

func CreativeUploadAsset(c *gin.Context) {
	creativeSetPrivateJSONHeaders(c)
	if !creativeRequireSession(c) {
		return
	}
	if strings.TrimSpace(c.GetHeader("Authorization")) != "" {
		creativeAPIError(c, http.StatusForbidden, "creative asset API requires a browser session")
		return
	}
	if c.Request.ContentLength <= 0 {
		creativeAPIError(c, http.StatusBadRequest, "Content-Length is required")
		return
	}
	if c.Request.ContentLength > service.CreativeAssetMaxBytes+(1<<20) {
		creativeAPIError(c, http.StatusRequestEntityTooLarge, "creative asset is too large")
		return
	}

	upload, ok := creativeReadAssetMultipart(c)
	if !ok {
		return
	}
	runtime := service.CurrentCreativeAssetRuntime()
	asset, duplicate, err := runtime.CreateOrGet(c.Request.Context(), c.GetInt("id"), service.CreativeAssetCreateRequest{
		Reader:          bytes.NewReader(upload.data),
		Size:            int64(len(upload.data)),
		ClientMimeType:  upload.mimeType,
		ClientMediaType: upload.mediaType,
		Metadata:        upload.metadata,
	})
	if err != nil {
		creativeAssetError(c, err)
		return
	}
	status := http.StatusCreated
	if duplicate {
		status = http.StatusOK
	}
	c.JSON(status, gin.H{"success": true, "message": "", "data": gin.H{"asset": service.CreativeAssetPublicResponse(asset)}})
}

func CreativeGetAsset(c *gin.Context) {
	creativeSetPrivateJSONHeaders(c)
	if !creativeRequireSession(c) {
		return
	}
	if strings.TrimSpace(c.GetHeader("Authorization")) != "" {
		creativeAPIError(c, http.StatusForbidden, "creative asset API requires a browser session")
		return
	}
	asset, err := service.CurrentCreativeAssetRuntime().Get(c.Request.Context(), c.GetInt("id"), c.Param("id"))
	if err != nil {
		creativeAssetError(c, err)
		return
	}
	creativeAPISuccess(c, gin.H{"asset": service.CreativeAssetPublicResponse(asset)})
}

func CreativeGetAssetContent(c *gin.Context) {
	if !creativeRequireSession(c) {
		return
	}
	if strings.TrimSpace(c.GetHeader("Authorization")) != "" {
		creativeAPIError(c, http.StatusForbidden, "creative asset API requires a browser session")
		return
	}
	content, err := service.CurrentCreativeAssetRuntime().OpenContent(c.Request.Context(), c.GetInt("id"), c.Param("id"), c.GetHeader("Range"))
	if err != nil {
		if errors.Is(err, service.ErrCreativeAssetRangeNotSatisfiable) {
			creativeSetPrivateContentHeaders(c)
			c.Header("Content-Range", "bytes */*")
			c.Status(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		creativeAssetError(c, err)
		return
	}
	defer content.Body.Close()
	creativeSetPrivateContentHeaders(c)
	c.Header("Content-Type", content.MimeType)
	c.Header("Accept-Ranges", "bytes")
	length := content.RangeEnd - content.RangeStart + 1
	if length < 0 {
		length = 0
	}
	c.Header("Content-Length", strconv.FormatInt(length, 10))
	if content.ContentRange != "" {
		c.Header("Content-Range", content.ContentRange)
	}
	c.Status(content.StatusCode)
	_, _ = io.Copy(c.Writer, content.Body)
}

func CreativeDeleteAsset(c *gin.Context) {
	creativeSetPrivateJSONHeaders(c)
	if !creativeRequireSession(c) {
		return
	}
	if strings.TrimSpace(c.GetHeader("Authorization")) != "" {
		creativeAPIError(c, http.StatusForbidden, "creative asset API requires a browser session")
		return
	}
	if err := service.CurrentCreativeAssetRuntime().DeleteIfUnreferenced(c.Request.Context(), c.GetInt("id"), c.Param("id")); err != nil {
		creativeAssetError(c, err)
		return
	}
	creativeAPISuccess(c, gin.H{"id": c.Param("id")})
}

type creativeAssetMultipartUpload struct {
	data      []byte
	mimeType  string
	mediaType string
	metadata  map[string]string
}

func creativeReadAssetMultipart(c *gin.Context) (creativeAssetMultipartUpload, bool) {
	contentType := c.GetHeader("Content-Type")
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil || !strings.HasPrefix(mediaType, "multipart/") {
		creativeAPIError(c, http.StatusBadRequest, "multipart/form-data is required")
		return creativeAssetMultipartUpload{}, false
	}
	reader, err := c.Request.MultipartReader()
	if err != nil {
		creativeAPIError(c, http.StatusBadRequest, "multipart/form-data is invalid")
		return creativeAssetMultipartUpload{}, false
	}

	upload := creativeAssetMultipartUpload{metadata: map[string]string{}}
	parts := 0
	fileSeen := false
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			creativeAPIError(c, http.StatusBadRequest, "multipart/form-data is invalid")
			return creativeAssetMultipartUpload{}, false
		}
		parts++
		if parts > creativeAssetMultipartPartLimit {
			creativeAPIError(c, http.StatusBadRequest, "too many multipart parts")
			return creativeAssetMultipartUpload{}, false
		}
		name := strings.TrimSpace(part.FormName())
		switch name {
		case "file":
			if fileSeen {
				creativeAPIError(c, http.StatusBadRequest, "only one file part is allowed")
				return creativeAssetMultipartUpload{}, false
			}
			fileSeen = true
			data, err := serviceReadAssetPart(part, service.CreativeAssetMaxBytes)
			if err != nil {
				creativeAssetError(c, err)
				return creativeAssetMultipartUpload{}, false
			}
			upload.data = data
			upload.mimeType = strings.TrimSpace(part.Header.Get("Content-Type"))
		case "mediaType":
			value, ok := creativeReadMultipartField(c, part)
			if !ok {
				return creativeAssetMultipartUpload{}, false
			}
			upload.mediaType = value
		case "clientAssetId":
			value, ok := creativeReadMultipartField(c, part)
			if !ok {
				return creativeAssetMultipartUpload{}, false
			}
			if value != "" {
				upload.metadata["clientAssetId"] = value
			}
		case "sourceUrl", "sourceURL", "url":
			creativeAPIError(c, http.StatusBadRequest, "sourceUrl is not accepted")
			return creativeAssetMultipartUpload{}, false
		default:
			if name != "" {
				creativeAPIError(c, http.StatusBadRequest, fmt.Sprintf("unsupported multipart field %s", name))
				return creativeAssetMultipartUpload{}, false
			}
		}
	}
	if !fileSeen || len(upload.data) == 0 {
		creativeAPIError(c, http.StatusBadRequest, "file is required")
		return creativeAssetMultipartUpload{}, false
	}
	return upload, true
}

func serviceReadAssetPart(reader io.Reader, maxBytes int64) ([]byte, error) {
	var buffer bytes.Buffer
	n, err := io.Copy(&buffer, io.LimitReader(reader, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if n > maxBytes {
		return nil, service.ErrCreativeAssetTooLarge
	}
	return buffer.Bytes(), nil
}

func creativeReadMultipartField(c *gin.Context, reader io.Reader) (string, bool) {
	var buffer bytes.Buffer
	n, err := io.Copy(&buffer, io.LimitReader(reader, creativeAssetMultipartFieldLimit+1))
	if err != nil {
		creativeAPIError(c, http.StatusBadRequest, "multipart field is invalid")
		return "", false
	}
	if n > creativeAssetMultipartFieldLimit {
		creativeAPIError(c, http.StatusBadRequest, "multipart field is too large")
		return "", false
	}
	return strings.TrimSpace(buffer.String()), true
}

func creativeAssetError(c *gin.Context, err error) {
	status := http.StatusInternalServerError
	message := "creative asset error"
	switch {
	case errors.Is(err, service.ErrCreativeAssetDisabled):
		status = http.StatusServiceUnavailable
		message = "creative asset sync is disabled"
	case errors.Is(err, service.ErrCreativeAssetNotFound):
		status = http.StatusNotFound
		message = "creative asset not found"
	case errors.Is(err, service.ErrCreativeAssetTooLarge):
		status = http.StatusRequestEntityTooLarge
		message = "creative asset is too large"
	case errors.Is(err, service.ErrCreativeAssetQuotaExceeded):
		status = http.StatusTooManyRequests
		message = "creative_asset_quota_exceeded"
	case errors.Is(err, service.ErrCreativeAssetInvalid):
		status = http.StatusBadRequest
		message = err.Error()
	case errors.Is(err, service.ErrCreativeAssetReferenced):
		status = http.StatusConflict
		message = "creative asset is referenced"
	case errors.Is(err, service.ErrCreativeAssetRangeNotSatisfiable):
		status = http.StatusRequestedRangeNotSatisfiable
		message = "creative asset range is not satisfiable"
	default:
		message = "creative asset storage error"
	}
	creativeAPIError(c, status, message)
}

func creativeSetPrivateJSONHeaders(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	c.Header("Pragma", "no-cache")
	c.Header("X-Content-Type-Options", "nosniff")
}

func creativeSetPrivateContentHeaders(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	c.Header("Vary", "Cookie")
	c.Header("X-Content-Type-Options", "nosniff")
}
