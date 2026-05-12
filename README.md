# Visual Assistant API

Go `net/http` service for uploading images and asking mocked visual-assistant questions about them. Chat history is stored in PostgreSQL and survives container restarts through the Compose-managed database volume.

## Run

```bash
docker compose up --build -d
curl -f http://localhost:8080/health
docker compose down
```

The API listens on port `8080` by default.

## Endpoints

### Health

```bash
curl http://localhost:8080/health
```

### Upload

```bash
curl -F "image=@example.png" http://localhost:8080/upload
```

Returns:

```json
{
  "image_id": "img_...",
  "content_type": "image/png",
  "size_bytes": 123
}
```

### Chat

```bash
curl -X POST http://localhost:8080/chat/img_... \
  -H "Content-Type: application/json" \
  -d "{\"prompt\":\"What is visible in this image?\"}"
```

### Stream Chat

```bash
curl -N -X POST http://localhost:8080/chat-stream/img_... \
  -H "Content-Type: application/json" \
  -d "{\"prompt\":\"Describe this image.\"}"
```

### History

```bash
curl http://localhost:8080/chat/img_...
```

## Development

```bash
go test ./...
go build ./...
```

PostgreSQL integration tests are skipped by default. Run them against a test database with:

```bash
DATABASE_URL="postgres://visual:visual@localhost:5432/visual_assistant?sslmode=disable" go test ./internal/store
```
"# visual-assistant" 
