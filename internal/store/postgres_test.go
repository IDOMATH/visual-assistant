package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestPostgresStoreIntegration(t *testing.T) {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set DATABASE_URL to run Postgres integration tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	schema := fmt.Sprintf("visual_assistant_test_%d", time.Now().UnixNano())
	if _, err := db.ExecContext(ctx, `CREATE SCHEMA `+quoteIdent(schema)); err != nil {
		t.Fatal(err)
	}
	defer db.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+quoteIdent(schema)+` CASCADE`)

	if _, err := db.ExecContext(ctx, `SET search_path TO `+quoteIdent(schema)); err != nil {
		t.Fatal(err)
	}

	pgStore := NewPostgres(db)
	if err := pgStore.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := pgStore.Migrate(ctx); err != nil {
		t.Fatalf("second migrate should be a no-op: %v", err)
	}

	imageID := "img_22222222222222222222222222222222"
	if err := pgStore.SaveImage(ctx, Image{
		ID:               imageID,
		ContentType:      "image/png",
		OriginalFilename: "pixel.png",
		SizeBytes:        4,
		Data:             []byte{0x89, 0x50, 0x4e, 0x47},
	}); err != nil {
		t.Fatal(err)
	}

	image, err := pgStore.GetImage(ctx, imageID)
	if err != nil {
		t.Fatal(err)
	}
	if image.ID != imageID || image.ContentType != "image/png" || image.OriginalFilename != "pixel.png" {
		t.Fatalf("unexpected image metadata: %+v", image)
	}

	message, err := pgStore.AppendExchange(ctx, imageID, "what is this?", "a mock answer")
	if err != nil {
		t.Fatal(err)
	}
	if message.Role != "assistant" || message.Content != "a mock answer" {
		t.Fatalf("unexpected assistant message: %+v", message)
	}

	messages, err := pgStore.GetMessages(ctx, imageID)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || messages[0].Role != "user" || messages[1].Role != "assistant" {
		t.Fatalf("unexpected messages: %+v", messages)
	}
}

func quoteIdent(identifier string) string {
	return `"` + strings.ReplaceAll(identifier, `"`, `""`) + `"`
}
