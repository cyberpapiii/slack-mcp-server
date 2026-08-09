package provider

import (
	"context"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/slack-go/slack"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeMessageFilesClient struct {
	calls       []string
	uploadData  []byte
	uploadType  string
	uploadErr   error
	completeErr error
}

func (f *fakeMessageFilesClient) GetUploadURLExternalContext(context.Context, slack.GetUploadURLExternalParameters) (*slack.GetUploadURLExternalResponse, error) {
	f.calls = append(f.calls, "get")
	return &slack.GetUploadURLExternalResponse{UploadURL: "https://upload.example", FileID: "F1"}, nil
}
func (f *fakeMessageFilesClient) UploadExternalBytes(_ context.Context, _ string, filename string, data []byte) error {
	f.calls = append(f.calls, "bytes")
	f.uploadData, f.uploadType = data, filename
	return f.uploadErr
}
func (f *fakeMessageFilesClient) CompleteUploadExternalContext(_ context.Context, params slack.CompleteUploadExternalParameters) (*slack.CompleteUploadExternalResponse, error) {
	f.calls = append(f.calls, "complete")
	if f.completeErr != nil {
		return nil, f.completeErr
	}
	return &slack.CompleteUploadExternalResponse{Files: params.Files}, nil
}
func (*fakeMessageFilesClient) ScheduleMessageContext(context.Context, string, string, ...slack.MsgOption) (string, string, error) {
	return "C1", "Q1", nil
}
func (*fakeMessageFilesClient) UpdateMessageContext(context.Context, string, string, ...slack.MsgOption) (string, string, string, error) {
	return "C1", "1.000001", "", nil
}
func (*fakeMessageFilesClient) DeleteMessageContext(context.Context, string, string) (string, string, error) {
	return "C1", "1.000001", nil
}
func (*fakeMessageFilesClient) GetConversationHistoryContext(context.Context, *slack.GetConversationHistoryParameters) (*slack.GetConversationHistoryResponse, error) {
	return &slack.GetConversationHistoryResponse{}, nil
}

func TestUnitMessageFilesUploadRunsExternalPipelineOnce(t *testing.T) {
	client := &fakeMessageFilesClient{}
	result, err := NewMessageFilesProvider(client).Upload(context.Background(), FileUploadRequest{
		Filename: "proof.png", Title: "Proof", ContentType: "image/png", Data: []byte("png"), ChannelID: "C1",
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"get", "bytes", "complete"}, client.calls)
	assert.Equal(t, []byte("png"), client.uploadData)
	assert.Equal(t, "proof.png", client.uploadType)
	assert.Equal(t, "F1", result.FileID)
}

func TestUnitMessageFilesUploadNeverCompletesAfterByteFailure(t *testing.T) {
	client := &fakeMessageFilesClient{uploadErr: errors.New("timeout")}
	_, err := NewMessageFilesProvider(client).Upload(context.Background(), FileUploadRequest{Filename: "a.txt", Data: []byte("x")})
	require.Error(t, err)
	assert.Equal(t, []string{"get", "bytes"}, client.calls)
}

func TestUnitUploadExternalBytesPostsMultipartFileOnce(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		calls++
		assert.Equal(t, http.MethodPost, request.Method)
		mediaType := request.Header.Get("Content-Type")
		_, params, err := mime.ParseMediaType(mediaType)
		require.NoError(t, err)
		reader := multipart.NewReader(request.Body, params["boundary"])
		part, err := reader.NextPart()
		require.NoError(t, err)
		assert.Equal(t, "proof.pdf", part.FileName())
		body, err := io.ReadAll(part)
		require.NoError(t, err)
		assert.Equal(t, []byte("pdf"), body)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := slack.New("xoxp-test", slack.OptionHTTPClient(server.Client()))
	err := (&MCPSlackClient{slackClient: client}).UploadExternalBytes(context.Background(), server.URL, "proof.pdf", []byte("pdf"))
	require.NoError(t, err)
	assert.Equal(t, 1, calls)
}
