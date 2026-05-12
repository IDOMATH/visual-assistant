FROM golang:1.26-alpine AS build

WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/visual-assistant ./cmd/server

FROM alpine:3.22

RUN adduser -D -H -u 10001 appuser
USER appuser
WORKDIR /app

COPY --from=build /out/visual-assistant /app/visual-assistant

EXPOSE 8080
ENTRYPOINT ["/app/visual-assistant"]
