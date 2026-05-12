package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

type Postgres struct {
	db *sql.DB
}

func NewPostgres(db *sql.DB) *Postgres {
	return &Postgres{db: db}
}

func (p *Postgres) Ping(ctx context.Context) error {
	return p.db.PingContext(ctx)
}

func (p *Postgres) Migrate(ctx context.Context) error {
	if _, err := p.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS schema_migrations (
	version integer PRIMARY KEY,
	name text NOT NULL,
	applied_at timestamptz NOT NULL DEFAULT now()
);
`); err != nil {
		return err
	}

	for _, migration := range migrations {
		if err := p.applyMigration(ctx, migration); err != nil {
			return err
		}
	}
	return nil
}

func (p *Postgres) applyMigration(ctx context.Context, migration migration) error {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(836724601)`); err != nil {
		return err
	}

	var applied bool
	if err := tx.QueryRowContext(
		ctx,
		`SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)`,
		migration.version,
	).Scan(&applied); err != nil {
		return err
	}
	if applied {
		return tx.Commit()
	}

	if _, err := tx.ExecContext(ctx, migration.up); err != nil {
		return fmt.Errorf("apply migration %03d %s: %w", migration.version, migration.name, err)
	}
	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO schema_migrations (version, name) VALUES ($1, $2)`,
		migration.version,
		migration.name,
	); err != nil {
		return err
	}
	return tx.Commit()
}

type migration struct {
	version int
	name    string
	up      string
}

var migrations = []migration{
	{
		version: 1,
		name:    "create_images_and_messages",
		up: `
CREATE TABLE images (
	id text PRIMARY KEY,
	content_type text NOT NULL,
	original_filename text,
	size_bytes bigint NOT NULL,
	data bytea NOT NULL,
	created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE messages (
	id bigserial PRIMARY KEY,
	image_id text NOT NULL REFERENCES images(id) ON DELETE CASCADE,
	role text NOT NULL CHECK (role IN ('user', 'assistant')),
	content text NOT NULL,
	created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX messages_image_id_created_at_idx
	ON messages (image_id, created_at, id);
`,
	},
}

func (p *Postgres) SaveImage(ctx context.Context, image Image) error {
	_, err := p.db.ExecContext(
		ctx,
		`INSERT INTO images (id, content_type, original_filename, size_bytes, data)
		 VALUES ($1, $2, NULLIF($3, ''), $4, $5)`,
		image.ID,
		image.ContentType,
		image.OriginalFilename,
		image.SizeBytes,
		image.Data,
	)
	return err
}

func (p *Postgres) GetImage(ctx context.Context, imageID string) (ImageMetadata, error) {
	var image ImageMetadata
	var filename sql.NullString
	err := p.db.QueryRowContext(
		ctx,
		`SELECT id, content_type, original_filename, size_bytes, created_at
		 FROM images
		 WHERE id = $1`,
		imageID,
	).Scan(&image.ID, &image.ContentType, &filename, &image.SizeBytes, &image.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ImageMetadata{}, ErrNotFound
	}
	if err != nil {
		return ImageMetadata{}, err
	}
	image.OriginalFilename = filename.String
	return image, nil
}

func (p *Postgres) GetMessages(ctx context.Context, imageID string) ([]Message, error) {
	if _, err := p.GetImage(ctx, imageID); err != nil {
		return nil, err
	}

	rows, err := p.db.QueryContext(
		ctx,
		`SELECT id, image_id, role, content, created_at
		 FROM messages
		 WHERE image_id = $1
		 ORDER BY created_at ASC, id ASC`,
		imageID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var messages []Message
	for rows.Next() {
		var message Message
		if err := rows.Scan(&message.ID, &message.ImageID, &message.Role, &message.Content, &message.CreatedAt); err != nil {
			return nil, err
		}
		messages = append(messages, message)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return messages, nil
}

func (p *Postgres) AppendExchange(ctx context.Context, imageID, userPrompt, assistantResponse string) (Message, error) {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return Message{}, err
	}
	defer tx.Rollback()

	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM images WHERE id = $1)`, imageID).Scan(&exists); err != nil {
		return Message{}, err
	}
	if !exists {
		return Message{}, ErrNotFound
	}

	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO messages (image_id, role, content) VALUES ($1, 'user', $2)`,
		imageID,
		userPrompt,
	); err != nil {
		return Message{}, err
	}

	var message Message
	if err := tx.QueryRowContext(
		ctx,
		`INSERT INTO messages (image_id, role, content)
		 VALUES ($1, 'assistant', $2)
		 RETURNING id, image_id, role, content, created_at`,
		imageID,
		assistantResponse,
	).Scan(&message.ID, &message.ImageID, &message.Role, &message.Content, &message.CreatedAt); err != nil {
		return Message{}, err
	}

	if err := tx.Commit(); err != nil {
		return Message{}, err
	}
	return message, nil
}
