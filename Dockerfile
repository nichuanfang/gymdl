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

# The base image supplies Python and media tooling required by download processors.
FROM --platform=linux/amd64 ghcr.io/nichuanfang/gymdl-base
WORKDIR /app
COPY requirements.txt ./
COPY config.yaml.example ./config.yaml.example
COPY --from=builder /app/app ./app

EXPOSE 8080 9000
ENTRYPOINT ["./app"]
