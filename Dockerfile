# syntax=docker/dockerfile:1

FROM ghcr.io/cirruslabs/flutter:stable AS frontend
WORKDIR /frontend
COPY frontend/ .
RUN flutter pub get && flutter build web --release --base-href /

FROM golang:1.23-bookworm AS backend
WORKDIR /src
COPY backend/ .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "-s -w" -o /out/minicloudstorage ./cmd/server

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
WORKDIR /app
COPY --from=backend /out/minicloudstorage /app/minicloudstorage
COPY --from=frontend /frontend/build/web /app/static
ENV STATIC_DIR=/app/static
EXPOSE 8080
ENTRYPOINT ["/app/minicloudstorage"]
