package assistant

import (
	"strings"
	"testing"

	"visual-assistant/internal/store"
)

func TestGenerateUsesImageAndHistoryContext(t *testing.T) {
	mock := NewMock()
	response := mock.Generate(
		store.ImageMetadata{
			ID:               "img_123",
			ContentType:      "image/png",
			OriginalFilename: "chart.png",
			SizeBytes:        42,
		},
		[]store.Message{
			{Role: "user", Content: "what is this?"},
			{Role: "assistant", Content: "a chart"},
		},
		"what changed?",
	)

	for _, want := range []string{"chart.png", "image/png", "42 bytes", "1 prior exchange", "what changed?"} {
		if !strings.Contains(response, want) {
			t.Fatalf("expected response to contain %q, got %q", want, response)
		}
	}
}

func TestGenerateTruncatesLongPrompts(t *testing.T) {
	mock := NewMock()
	response := mock.Generate(store.ImageMetadata{ID: "img_123"}, nil, strings.Repeat("word ", 25))

	if !strings.Contains(response, "...") {
		t.Fatalf("expected long prompt to be summarized, got %q", response)
	}
}
