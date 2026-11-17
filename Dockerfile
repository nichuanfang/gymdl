# Build WebUI assets first; Go embeds web/dist into the final executable.
FROM node:24-bookworm-slim AS webui-builder
WORKDIR /src/webui
COPY webui/package.json webui/package-lock.json ./
RUN npm ci
COPY webui/ ./
RUN npm run build

# Build the Linux/amd64 service used by the published Docker image.
FROM golang:1.24 AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=webui-builder /src/webui/dist/ /app/web/dist/
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o /app/app .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o /app/discovery-check ./cmd/discovery-check

# The base image supplies Python and media tooling required by download processors.
FROM --platform=linux/amd64 ghcr.io/nichuanfang/gymdl-base
WORKDIR /app
# fpcalc fingerprints local audio for the library duplicate review. It does not call AcoustID.
RUN apt-get update \
	&& apt-get install -y --no-install-recommends libchromaprint-tools \
	&& rm -rf /var/lib/apt/lists/*
# Personal likes and metadata checks also need yt-dlp before the first download.
ARG YT_DLP_VERSION=2026.10.26
RUN python3 -m pip install --no-cache-dir --break-system-packages "yt-dlp[default]==${YT_DLP_VERSION}"
COPY requirements.txt ./
COPY config.yaml.example ./config.yaml.example
COPY config.playlists.example.yaml ./config.playlists.example.yaml
COPY skills ./skills
COPY --from=builder /app/app ./app
COPY --from=builder /app/discovery-check ./discovery-check

EXPOSE 8080 9000
ENTRYPOINT ["./app"]
