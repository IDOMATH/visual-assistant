package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"visual-assistant/internal/assistant"
	"visual-assistant/internal/store"
)

func TestHealth(t *testing.T) {
	tests := []struct {
		name       string
		pingErr    error
		wantStatus int
	}{
		{name: "ready", wantStatus: http.StatusOK},
		{name: "not ready", pingErr: errors.New("down"), wantStatus: http.StatusServiceUnavailable},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := newFakeStore()
			fake.pingErr = tt.pingErr
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/health", nil)

			NewServer(fake, assistant.NewMock(), nil).Handler().ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
		})
	}
}

func TestRequestIDHeader(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.Header.Set("X-Request-ID", "req_test")

	NewServer(newFakeStore(), assistant.NewMock(), nil).Handler().ServeHTTP(rec, req)

	if got := rec.Header().Get("X-Request-ID"); got != "req_test" {
		t.Fatalf("X-Request-ID = %q, want req_test", got)
	}
}

func TestUploadStoresImage(t *testing.T) {
	fake := newFakeStore()
	body, contentType := multipartBody(t, "image", "pixel.png", pngBytes())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/upload", body)
	req.Header.Set("Content-Type", contentType)

	NewServer(fake, assistant.NewMock(), nil).Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var response uploadResponse
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.ImageID == "" || response.ContentType != "image/png" || response.SizeBytes == 0 {
		t.Fatalf("unexpected response: %+v", response)
	}
	if _, err := fake.GetImage(context.Background(), response.ImageID); err != nil {
		t.Fatalf("image was not stored: %v", err)
	}
}

func TestUploadRejectsNonImage(t *testing.T) {
	body, contentType := multipartBody(t, "image", "note.txt", []byte("hello"))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/upload", body)
	req.Header.Set("Content-Type", contentType)

	NewServer(newFakeStore(), assistant.NewMock(), nil).Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnsupportedMediaType)
	}
}

func TestChatPostPersistsExchange(t *testing.T) {
	fake := newFakeStore()
	imageID := fake.mustSeedImage()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/chat/"+imageID, strings.NewReader(`{"prompt":"what is visible?"}`))

	NewServer(fake, assistant.NewMock(), nil).Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	messages, err := fake.GetMessages(context.Background(), imageID)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || messages[0].Role != "user" || messages[1].Role != "assistant" {
		t.Fatalf("unexpected messages: %+v", messages)
	}
}

func TestChatValidationAndNotFound(t *testing.T) {
	tests := []struct {
		name       string
		imageID    string
		body       string
		wantStatus int
	}{
		{name: "unknown image", imageID: "img_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", body: `{"prompt":"hi"}`, wantStatus: http.StatusNotFound},
		{name: "invalid image id", imageID: "img_missing", body: `{"prompt":"hi"}`, wantStatus: http.StatusBadRequest},
		{name: "empty prompt", imageID: "img_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", body: `{"prompt":"   "}`, wantStatus: http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := newFakeStore()
			if tt.name == "empty prompt" {
				fake.mustSeedImageWithID(tt.imageID)
			}
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/chat/"+tt.imageID, strings.NewReader(tt.body))

			NewServer(fake, assistant.NewMock(), nil).Handler().ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestReadPromptRejectsTrailingJSON(t *testing.T) {
	fake := newFakeStore()
	imageID := fake.mustSeedImage()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/chat/"+imageID, strings.NewReader(`{"prompt":"hello"} {"prompt":"again"}`))

	NewServer(fake, assistant.NewMock(), nil).Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestIDAndFilenameValidation(t *testing.T) {
	imageID, err := newImageID()
	if err != nil {
		t.Fatal(err)
	}
	if !validImageID(imageID) {
		t.Fatalf("generated invalid image id: %q", imageID)
	}

	if got := cleanFilename(`C:\tmp\bad` + string(rune(1)) + `\pixel.png`); got != "pixel.png" {
		t.Fatalf("cleanFilename() = %q, want pixel.png", got)
	}
}

func TestGetHistory(t *testing.T) {
	fake := newFakeStore()
	imageID := fake.mustSeedImage()
	_, err := fake.AppendExchange(context.Background(), imageID, "hello", "answer")
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/chat/"+imageID, nil)

	NewServer(fake, assistant.NewMock(), nil).Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var response historyResponse
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if len(response.Messages) != 2 {
		t.Fatalf("messages = %d, want 2", len(response.Messages))
	}
}

func TestChatStreamEmitsEventsAndPersists(t *testing.T) {
	fake := newFakeStore()
	imageID := fake.mustSeedImage()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/chat-stream/"+imageID, strings.NewReader(`{"prompt":"describe this image"}`))

	NewServer(fake, assistant.NewMock(), nil).Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "event: chunk") || !strings.Contains(body, "event: done") {
		t.Fatalf("expected chunk and done events, got %q", body)
	}
	messages, err := fake.GetMessages(context.Background(), imageID)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 {
		t.Fatalf("messages = %d, want 2", len(messages))
	}
}

func multipartBody(t *testing.T, field, filename string, content []byte) (io.Reader, string) {
	t.Helper()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile(field, filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return &body, writer.FormDataContentType()
}

func pngBytes() []byte {
	return []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
		0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x02, 0x00, 0x00, 0x00, 0x90, 0x77, 0x53,
		0xde,
	}
}

type fakeStore struct {
	mu       sync.Mutex
	pingErr  error
	images   map[string]store.ImageMetadata
	messages map[string][]store.Message
	nextID   int64
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		images:   map[string]store.ImageMetadata{},
		messages: map[string][]store.Message{},
		nextID:   1,
	}
}

func (f *fakeStore) Ping(context.Context) error {
	return f.pingErr
}

func (f *fakeStore) SaveImage(_ context.Context, image store.Image) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.images[image.ID] = store.ImageMetadata{
		ID:               image.ID,
		ContentType:      image.ContentType,
		OriginalFilename: image.OriginalFilename,
		SizeBytes:        image.SizeBytes,
		CreatedAt:        time.Now().UTC(),
	}
	return nil
}

func (f *fakeStore) GetImage(_ context.Context, imageID string) (store.ImageMetadata, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	image, ok := f.images[imageID]
	if !ok {
		return store.ImageMetadata{}, store.ErrNotFound
	}
	return image, nil
}

func (f *fakeStore) GetMessages(_ context.Context, imageID string) ([]store.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.images[imageID]; !ok {
		return nil, store.ErrNotFound
	}
	messages := append([]store.Message(nil), f.messages[imageID]...)
	return messages, nil
}

func (f *fakeStore) AppendExchange(_ context.Context, imageID, userPrompt, assistantResponse string) (store.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.images[imageID]; !ok {
		return store.Message{}, store.ErrNotFound
	}

	now := time.Now().UTC()
	user := store.Message{ID: f.nextID, ImageID: imageID, Role: "user", Content: userPrompt, CreatedAt: now}
	f.nextID++
	assistantMessage := store.Message{ID: f.nextID, ImageID: imageID, Role: "assistant", Content: assistantResponse, CreatedAt: now.Add(time.Millisecond)}
	f.nextID++
	f.messages[imageID] = append(f.messages[imageID], user, assistantMessage)
	return assistantMessage, nil
}

func (f *fakeStore) mustSeedImage() string {
	return f.mustSeedImageWithID("img_11111111111111111111111111111111")
}

func (f *fakeStore) mustSeedImageWithID(imageID string) string {
	err := f.SaveImage(context.Background(), store.Image{
		ID:               imageID,
		ContentType:      "image/png",
		OriginalFilename: "pixel.png",
		SizeBytes:        int64(len(pngBytes())),
		Data:             pngBytes(),
	})
	if err != nil {
		panic(err)
	}
	return imageID
}
