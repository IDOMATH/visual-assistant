package store

import (
	"errors"
	"time"
)

var ErrNotFound = errors.New("not found")

type Image struct {
	ID               string
	ContentType      string
	OriginalFilename string
	SizeBytes        int64
	Data             []byte
	CreatedAt        time.Time
}

type ImageMetadata struct {
	ID               string
	ContentType      string
	OriginalFilename string
	SizeBytes        int64
	CreatedAt        time.Time
}

type Message struct {
	ID        int64     `json:"-"`
	ImageID   string    `json:"-"`
	Role      string    `json:"role"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
}
