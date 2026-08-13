package handler

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gocarina/gocsv"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/slack-go/slack"
	"go.uber.org/zap"
)

func (ch *ConversationsHandler) FilesGetHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logToolCall(ch.logger, "FilesGetHandler called", request)

	if ready, err := ch.apiProvider.IsReady(); !ready {
		ch.logger.Error("API provider not ready", zap.Error(err))
		return nil, err
	}

	params, err := ch.parseParamsToolFilesGet(request)
	if err != nil {
		ch.logger.Error("Failed to parse attachment_get_data params", zap.Error(err))
		return nil, err
	}

	fileInfo, _, _, err := ch.apiProvider.WebAPI().GetFileInfoContext(ctx, params.fileID, 0, 0)
	if err != nil {
		ch.logger.Error("Slack GetFileInfoContext failed", zap.Error(err))
		return nil, err
	}

	if fileInfo.Size > maxFileSizeBytes {
		return nil, fmt.Errorf("file size %d bytes exceeds maximum allowed size of %d bytes", fileInfo.Size, maxFileSizeBytes)
	}

	var buf bytes.Buffer
	downloadURL := fileInfo.URLPrivateDownload
	if downloadURL == "" {
		downloadURL = fileInfo.URLPrivate
	}
	if downloadURL == "" {
		return nil, errors.New("file has no downloadable URL")
	}

	err = ch.apiProvider.WebAPI().GetFileContext(ctx, downloadURL, &buf)
	if err != nil {
		ch.logger.Error("Slack GetFileContext failed", zap.Error(err))
		return nil, err
	}

	content := buf.Bytes()

	// Native MCP image avoids base64-in-JSON overflow.
	if isImageMimetype(fileInfo.Mimetype) {
		imageData := base64.StdEncoding.EncodeToString(content)
		metadata, err := marshalFileMetadata(fileMetadataPayload{
			FileID:   fileInfo.ID,
			Filename: fileInfo.Name,
			Mimetype: fileInfo.Mimetype,
			Size:     len(content),
		})
		if err != nil {
			ch.logger.Error("Failed to marshal attachment metadata", zap.Error(err))
			return nil, err
		}
		return mcp.NewToolResultImage(metadata, imageData, fileInfo.Mimetype), nil
	}

	encoding := "none"
	var contentStr string

	if isTextMimetype(fileInfo.Mimetype) {
		contentStr = string(content)
	} else {
		contentStr = base64.StdEncoding.EncodeToString(content)
		encoding = "base64"
	}

	result, err := marshalFileResult(fileResultPayload{
		FileID:   fileInfo.ID,
		Filename: fileInfo.Name,
		Mimetype: fileInfo.Mimetype,
		Size:     len(content),
		Encoding: encoding,
		Content:  contentStr,
	})
	if err != nil {
		ch.logger.Error("Failed to marshal attachment result", zap.Error(err))
		return nil, err
	}

	return mcp.NewToolResultText(result), nil
}

// fileMetadataPayload is the 4-key shape emitted alongside native MCP image
// content by attachment_get_data.
type fileMetadataPayload struct {
	FileID   string `json:"file_id"`
	Filename string `json:"filename"`
	Mimetype string `json:"mimetype"`
	Size     int    `json:"size"`
}

// fileResultPayload is the 6-key shape emitted for text and binary files by
// attachment_get_data. Encoding is always present, including when it is "none".
type fileResultPayload struct {
	FileID   string `json:"file_id"`
	Filename string `json:"filename"`
	Mimetype string `json:"mimetype"`
	Size     int    `json:"size"`
	Encoding string `json:"encoding"`
	Content  string `json:"content"`
}

func marshalFileMetadata(p fileMetadataPayload) (string, error) {
	b, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func marshalFileResult(p fileResultPayload) (string, error) {
	b, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func (ch *ConversationsHandler) parseParamsToolFilesList(request mcp.CallToolRequest) (*filesListParams, error) {
	params := &filesListParams{
		channel: request.GetString("channel_id", ""),
		user:    request.GetString("user_id", ""),
		types:   request.GetString("types", ""),
		cursor:  request.GetString("cursor", ""),
		limit:   50,
	}

	if limitStr := request.GetString("limit", ""); limitStr != "" {
		if v, err := strconv.Atoi(limitStr); err == nil && v > 0 {
			params.limit = v
		}
	}

	return params, nil
}

func (ch *ConversationsHandler) FilesListHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logToolCall(ch.logger, "FilesListHandler called", request)

	if ready, err := ch.apiProvider.IsReady(); !ready {
		ch.logger.Error("API provider not ready", zap.Error(err))
		return nil, err
	}

	params, err := ch.parseParamsToolFilesList(request)
	if err != nil {
		ch.logger.Error("Failed to parse files_list params", zap.Error(err))
		return nil, err
	}

	listParams := slack.ListFilesParameters{
		Channel: params.channel,
		User:    params.user,
		Types:   params.types,
		Limit:   params.limit,
		Cursor:  params.cursor,
	}

	files, nextPage, err := ch.apiProvider.WebAPI().ListFilesContext(ctx, listParams)
	if err != nil {
		ch.logger.Error("Slack ListFilesContext failed", zap.Error(err))
		return nil, err
	}

	if len(files) == 0 {
		return mcp.NewToolResultText("No files found."), nil
	}

	nextCursor := ""
	if nextPage != nil {
		nextCursor = nextPage.Cursor
	}

	results := make([]FileListResult, 0, len(files))
	for i, f := range files {
		cursor := ""
		if i == len(files)-1 {
			cursor = nextCursor
		}
		results = append(results, FileListResult{
			FileID:     f.ID,
			Name:       f.Name,
			Title:      f.Title,
			Mimetype:   f.Mimetype,
			Filetype:   f.Filetype,
			PrettyType: f.PrettyType,
			Size:       f.Size,
			UserID:     f.User,
			Created:    f.Created.Time().Format(time.RFC3339),
			Permalink:  f.Permalink,
			Cursor:     cursor,
		})
	}

	csvBytes, err := gocsv.MarshalBytes(&results)
	if err != nil {
		ch.logger.Error("Failed to marshal files to CSV", zap.Error(err))
		return nil, err
	}

	return mcp.NewToolResultText(string(csvBytes)), nil
}

func isImageMimetype(mimetype string) bool {
	return strings.HasPrefix(mimetype, "image/")
}

func isTextMimetype(mimetype string) bool {
	if strings.HasPrefix(mimetype, "text/") {
		return true
	}
	textMimetypes := map[string]bool{
		"application/json":       true,
		"application/xml":        true,
		"application/javascript": true,
		"application/x-yaml":     true,
		"application/x-sh":       true,
	}
	return textMimetypes[mimetype]
}

// ConversationsHistoryHandler streams conversation history as CSV

func (ch *ConversationsHandler) parseParamsToolFilesGet(request mcp.CallToolRequest) (*filesGetParams, error) {
	if !requireToolEnabled("SLACK_MCP_ATTACHMENT_TOOL", "attachment_get_data") {
		ch.logger.Error("Attachment tool disabled by default")
		return nil, errors.New(
			"by default, the attachment_get_data tool is disabled. " +
				"To enable it, set the SLACK_MCP_ATTACHMENT_TOOL environment variable to true or 1, " +
				"or add 'attachment_get_data' to SLACK_MCP_ENABLED_TOOLS",
		)
	}

	fileID := request.GetString("file_id", "")
	if fileID == "" {
		return nil, errors.New("file_id is required")
	}

	return &filesGetParams{
		fileID: fileID,
	}, nil
}
