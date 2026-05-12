package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path"
	"strings"
	"time"

	"visual-assistant/internal/store"
)

const (
	maxImageBytes  int64 = 10 << 20
	maxPromptBytes int64 = 16 << 10
)

type Server struct {
	store     Store
	assistant Assistant
	logger    *slog.Logger
}

type Store interface {
	Ping(ctx context.Context) error
	SaveImage(ctx context.Context, image store.Image) error
	GetImage(ctx context.Context, imageID string) (store.ImageMetadata, error)
	GetMessages(ctx context.Context, imageID string) ([]store.Message, error)
	AppendExchange(ctx context.Context, imageID, userPrompt, assistantResponse string) (store.Message, error)
}

type Assistant interface {
	Generate(image store.ImageMetadata, history []store.Message, prompt string) string
}

func NewServer(store Store, assistant Assistant, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{store: store, assistant: assistant, logger: logger}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/upload", s.handleUpload)
	mux.HandleFunc("/chat/", s.handleChat)
	mux.HandleFunc("/chat-stream/", s.handleChatStream)
	return s.logRequests(mux)
}

func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := r.Header.Get("X-Request-ID")
		if requestID == "" {
			requestID = newRequestID()
		}
		w.Header().Set("X-Request-ID", requestID)

		recorder := &responseRecorder{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()
		next.ServeHTTP(recorder, r)

		s.logger.Info("http_request",
			"request_id", requestID,
			"method", r.Method,
			"path", r.URL.Path,
			"status", recorder.status,
			"bytes", recorder.bytes,
			"duration_ms", time.Since(start).Milliseconds(),
			"remote_addr", r.RemoteAddr,
			"user_agent", r.UserAgent(),
		)
	})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if err := s.store.Ping(r.Context()); err != nil {
		writeError(w, http.StatusServiceUnavailable, "service is not ready")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxImageBytes+(1<<20))
	if err := r.ParseMultipartForm(maxImageBytes); err != nil {
		writeError(w, http.StatusBadRequest, "expected multipart form with an image file")
		return
	}

	file, header, err := r.FormFile("image")
	if err != nil {
		writeError(w, http.StatusBadRequest, "missing image file field")
		return
	}
	defer file.Close()

	data, err := readLimited(file, maxImageBytes)
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "image exceeds 10 MB limit")
		return
	}
	contentType := detectImageContentType(data)
	if contentType == "" {
		writeError(w, http.StatusUnsupportedMediaType, "unsupported image content type")
		return
	}

	imageID, err := newImageID()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create image id")
		return
	}

	image := store.Image{
		ID:               imageID,
		ContentType:      contentType,
		OriginalFilename: cleanFilename(header.Filename),
		SizeBytes:        int64(len(data)),
		Data:             data,
	}
	if err := s.store.SaveImage(r.Context(), image); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to store image")
		return
	}

	writeJSON(w, http.StatusCreated, uploadResponse{
		ImageID:     image.ID,
		ContentType: image.ContentType,
		SizeBytes:   image.SizeBytes,
	})
}

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	imageID := strings.TrimPrefix(r.URL.Path, "/chat/")
	if imageID == "" || strings.Contains(imageID, "/") {
		http.NotFound(w, r)
		return
	}
	if !validImageID(imageID) {
		writeError(w, http.StatusBadRequest, "invalid image id")
		return
	}

	switch r.Method {
	case http.MethodGet:
		s.handleGetHistory(w, r, imageID)
	case http.MethodPost:
		s.handleChatPost(w, r, imageID)
	default:
		methodNotAllowed(w)
	}
}

func (s *Server) handleGetHistory(w http.ResponseWriter, r *http.Request, imageID string) {
	messages, err := s.store.GetMessages(r.Context(), imageID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "image not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to retrieve history")
		return
	}
	writeJSON(w, http.StatusOK, historyResponse{ImageID: imageID, Messages: messages})
}

func (s *Server) handleChatPost(w http.ResponseWriter, r *http.Request, imageID string) {
	prompt, ok := readPrompt(w, r)
	if !ok {
		return
	}

	image, history, ok := s.loadConversationContext(w, r, imageID)
	if !ok {
		return
	}

	response := s.assistant.Generate(image, history, prompt)
	message, err := s.store.AppendExchange(r.Context(), imageID, prompt, response)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "image not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to store message")
		return
	}

	writeJSON(w, http.StatusOK, chatResponse{ImageID: imageID, Message: message})
}

func (s *Server) handleChatStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	imageID := strings.TrimPrefix(r.URL.Path, "/chat-stream/")
	if imageID == "" || strings.Contains(imageID, "/") {
		http.NotFound(w, r)
		return
	}
	if !validImageID(imageID) {
		writeError(w, http.StatusBadRequest, "invalid image id")
		return
	}

	prompt, ok := readPrompt(w, r)
	if !ok {
		return
	}

	image, history, ok := s.loadConversationContext(w, r, imageID)
	if !ok {
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming is not supported")
		return
	}

	response := s.assistant.Generate(image, history, prompt)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	for _, chunk := range splitStream(response) {
		if err := writeSSE(w, "chunk", streamChunk{Content: chunk}); err != nil {
			return
		}
		flusher.Flush()
		time.Sleep(25 * time.Millisecond)
	}

	persistCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	message, err := s.store.AppendExchange(persistCtx, imageID, prompt, response)
	if err != nil {
		_ = writeSSE(w, "error", errorResponse{Error: "failed to store message"})
		flusher.Flush()
		return
	}

	_ = writeSSE(w, "done", chatResponse{ImageID: imageID, Message: message})
	flusher.Flush()
}

func (s *Server) loadConversationContext(w http.ResponseWriter, r *http.Request, imageID string) (store.ImageMetadata, []store.Message, bool) {
	image, err := s.store.GetImage(r.Context(), imageID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "image not found")
		return store.ImageMetadata{}, nil, false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to retrieve image")
		return store.ImageMetadata{}, nil, false
	}

	history, err := s.store.GetMessages(r.Context(), imageID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "image not found")
		return store.ImageMetadata{}, nil, false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to retrieve history")
		return store.ImageMetadata{}, nil, false
	}
	return image, history, true
}

func readPrompt(w http.ResponseWriter, r *http.Request) (string, bool) {
	var request promptRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxPromptBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON request body")
		return "", false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "request body must contain one JSON object")
		return "", false
	}
	if strings.TrimSpace(request.Prompt) == "" {
		writeError(w, http.StatusBadRequest, "prompt must not be empty")
		return "", false
	}
	return strings.TrimSpace(request.Prompt), true
}

func readLimited(reader io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("payload too large")
	}
	return data, nil
}

func detectImageContentType(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	contentType := http.DetectContentType(data)
	switch contentType {
	case "image/jpeg", "image/png", "image/gif", "image/webp", "image/bmp":
		return contentType
	default:
		return ""
	}
}

func cleanFilename(filename string) string {
	filename = strings.TrimSpace(strings.ReplaceAll(filename, "\\", "/"))
	filename = path.Base(filename)
	if filename == "." || filename == "/" {
		return ""
	}

	var builder strings.Builder
	for _, r := range filename {
		if r < 32 || r == 127 || r == '/' || r == '\\' {
			continue
		}
		builder.WriteRune(r)
	}
	return builder.String()
}

func validImageID(imageID string) bool {
	if len(imageID) != len("img_")+32 || !strings.HasPrefix(imageID, "img_") {
		return false
	}
	for _, r := range imageID[len("img_"):] {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

func newImageID() (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	return "img_" + hex.EncodeToString(bytes[:]), nil
}

func newRequestID() string {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "req_unavailable"
	}
	return "req_" + hex.EncodeToString(bytes[:])
}

func splitStream(response string) []string {
	words := strings.Fields(response)
	if len(words) == 0 {
		return []string{response}
	}

	chunks := make([]string, 0, (len(words)+3)/4)
	for i := 0; i < len(words); i += 4 {
		end := i + 4
		if end > len(words) {
			end = len(words)
		}
		chunks = append(chunks, strings.Join(words[i:end], " "))
	}
	return chunks
}

func writeSSE(w io.Writer, event string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
	return err
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, errorResponse{Error: message})
}

func methodNotAllowed(w http.ResponseWriter) {
	writeError(w, http.StatusMethodNotAllowed, "method not allowed")
}

type promptRequest struct {
	Prompt string `json:"prompt"`
}

type uploadResponse struct {
	ImageID     string `json:"image_id"`
	ContentType string `json:"content_type"`
	SizeBytes   int64  `json:"size_bytes"`
}

type historyResponse struct {
	ImageID  string          `json:"image_id"`
	Messages []store.Message `json:"messages"`
}

type chatResponse struct {
	ImageID string        `json:"image_id"`
	Message store.Message `json:"message"`
}

type streamChunk struct {
	Content string `json:"content"`
}

type errorResponse struct {
	Error string `json:"error"`
}

type responseRecorder struct {
	http.ResponseWriter
	status      int
	bytes       int
	wroteHeader bool
}

func (r *responseRecorder) WriteHeader(status int) {
	if r.wroteHeader {
		return
	}
	r.status = status
	r.wroteHeader = true
	r.ResponseWriter.WriteHeader(status)
}

func (r *responseRecorder) Write(data []byte) (int, error) {
	if !r.wroteHeader {
		r.wroteHeader = true
	}
	n, err := r.ResponseWriter.Write(data)
	r.bytes += n
	return n, err
}

func (r *responseRecorder) Flush() {
	if flusher, ok := r.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}
