package handler

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

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
	if buf.Len() > maxFileSizeBytes {
		return nil, fmt.Errorf("downloaded %d bytes, exceeds maximum allowed size of %d bytes", buf.Len(), maxFileSizeBytes)
	}

	content := buf.Bytes()

	// Native MCP image avoids base64-in-JSON overflow.
	if isImageMimetype(fileInfo.Mimetype) {
		imageData := base64.StdEncoding.EncodeToString(content)
		metadata := fallbackJSON(fileMetadataPayload{
			FileID:   fileInfo.ID,
			Filename: fileInfo.Name,
			Mimetype: fileInfo.Mimetype,
			Size:     len(content),
		})
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

	return mcp.NewToolResultText(fallbackJSON(fileResultPayload{
		FileID:   fileInfo.ID,
		Filename: fileInfo.Name,
		Mimetype: fileInfo.Mimetype,
		Size:     len(content),
		Encoding: encoding,
		Content:  contentStr,
	})), nil
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

func (ch *ConversationsHandler) parseParamsToolFilesList(request mcp.CallToolRequest) (*filesListParams, error) {
	page, err := filesPage(request.GetString("cursor", ""))
	if err != nil {
		return nil, err
	}
	return &filesListParams{
		channel: request.GetString("channel_id", ""),
		user:    request.GetString("user_id", ""),
		types:   request.GetString("types", ""),
		limit:   pageLimit(request, 50, 200),
		page:    page,
	}, nil
}

// files.list paginates by count/page, not by cursor (Slack ignores `limit`
// and never returns response_metadata). The cursor this tool hands out is the
// next page number, so `cursor` round-trips through the shared CSV contract.
func filesPage(cursor string) (int, error) {
	if cursor == "" {
		return 1, nil
	}
	page, err := strconv.Atoi(cursor)
	if err != nil || page < 1 {
		return 0, invalidArguments("cursor must be a #next_cursor value from a previous files_list call")
	}
	return page, nil
}

func nextFilesCursor(paging *slack.Paging) string {
	if paging == nil || paging.Page >= paging.Pages {
		return ""
	}
	return strconv.Itoa(paging.Page + 1)
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

	files, paging, err := ch.apiProvider.WebAPI().GetFilesContext(ctx, slack.GetFilesParameters{
		Channel: params.channel,
		User:    params.user,
		Types:   params.types,
		Count:   params.limit,
		Page:    params.page,
	})
	if err != nil {
		ch.logger.Error("Slack GetFilesContext failed", zap.Error(err))
		return nil, err
	}

	if len(files) == 0 {
		return mcp.NewToolResultText("No files found."), nil
	}

	nextCursor := nextFilesCursor(paging)

	resolver := ch.newUserResolver(ctx)
	userName := func(id string) string {
		name, realName, _ := resolver.resolve(id)
		if realName != "" {
			return realName
		}
		return name
	}
	rows := make([]fileRow, 0, len(files))
	for _, f := range files {
		rows = append(rows, fileRowFromSlack(f, params.channel, userName))
	}

	result, err := marshalFilesToCSV(rows, nextCursor, ch.channelDisplayName)
	if err != nil {
		ch.logger.Error("Failed to marshal files to CSV", zap.Error(err))
		return nil, err
	}
	return result, nil
}

// fileRow is one files_list CSV row. FileID feeds attachment_get_data;
// Channel and MsgID locate the message the file was shared in.
type fileRow struct {
	FileID   string `csv:"FileID"`
	Name     string `csv:"Name"`
	Filetype string `csv:"Filetype"`
	Size     int    `csv:"Size"`
	Created  string `csv:"Created"`
	User     string `csv:"User"`
	Channel  string `csv:"Channel"`
	MsgID    string `csv:"MsgID"`
}

func fileRowFromSlack(f slack.File, preferredChannel string, userName func(string) string) fileRow {
	channel, msgID := fileShare(f, preferredChannel)
	return fileRow{
		FileID:   f.ID,
		Name:     f.Name,
		Filetype: f.Filetype,
		Size:     f.Size,
		Created:  f.Created.Time().Format(time.RFC3339),
		User:     userName(f.User),
		Channel:  channel,
		MsgID:    msgID,
	}
}

// fileShare picks the conversation a file row reports: the channel the
// caller filtered on when the file is shared there, otherwise the first
// conversation Slack lists. msgID is that share's message timestamp when
// Slack reports one.
func fileShare(f slack.File, preferredChannel string) (channel, msgID string) {
	ids := make([]string, 0, len(f.Channels)+len(f.Groups)+len(f.IMs))
	ids = append(ids, f.Channels...)
	ids = append(ids, f.Groups...)
	ids = append(ids, f.IMs...)
	if len(ids) == 0 {
		return "", ""
	}
	channel = ids[0]
	for _, id := range ids {
		if id == preferredChannel {
			channel = id
		}
	}
	for _, shares := range []map[string][]slack.ShareFileInfo{f.Shares.Public, f.Shares.Private} {
		if infos := shares[channel]; len(infos) > 0 {
			return channel, infos[0].Ts
		}
	}
	return channel, ""
}

func marshalFilesToCSV(rows []fileRow, nextCursor string, channelName func(string) string) (*mcp.CallToolResult, error) {
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.Channel)
	}
	return marshalRowsToCSV(channelsLegend(ids, channelName), &rows, nextCursor)
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

func (ch *ConversationsHandler) parseParamsToolFilesGet(request mcp.CallToolRequest) (*filesGetParams, error) {

	fileID := request.GetString("file_id", "")
	if fileID == "" {
		return nil, errors.New("file_id is required")
	}

	return &filesGetParams{
		fileID: fileID,
	}, nil
}
