package assistant

import (
	"fmt"
	"strings"

	"visual-assistant/internal/store"
)

type Mock struct{}

func NewMock() Mock {
	return Mock{}
}

func (Mock) Generate(image store.ImageMetadata, history []store.Message, prompt string) string {
	prompt = strings.TrimSpace(prompt)
	priorExchanges := countUserMessages(history)
	name := image.OriginalFilename
	if name == "" {
		name = image.ID
	}

	return fmt.Sprintf(
		"I reviewed %s (%s, %d bytes) with %d prior exchange(s). Mock answer: %s",
		name,
		image.ContentType,
		image.SizeBytes,
		priorExchanges,
		summarizePrompt(prompt),
	)
}

func countUserMessages(history []store.Message) int {
	count := 0
	for _, message := range history {
		if message.Role == "user" {
			count++
		}
	}
	return count
}

func summarizePrompt(prompt string) string {
	words := strings.Fields(prompt)
	if len(words) == 0 {
		return "Please provide a non-empty question about the image."
	}
	if len(words) <= 18 {
		return prompt
	}
	return strings.Join(words[:18], " ") + "..."
}
