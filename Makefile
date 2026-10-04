
NAME=gymdl
VERSION=$(shell git describe --tags || echo "master")
RELEASE_DIR=release
GOBUILD=CGO_ENABLED=0 go build -trimpath -ldflags '-w -s -X "main.buildVersion=$(VERSION)"'

# 前端构建（输出到 web/dist 供 go:embed）
webui:
	cd webui && npm ci && npm run build
	rsync -a --delete webui/dist/ web/dist/

PLATFORM_LIST = \
	darwin-amd64 \
	darwin-arm64 \
	linux-amd64 \
	linux-arm64 \
	linux-arm

WINDOWS_ARCH_LIST = windows-amd64

all: linux-amd64 linux-arm64 linux-arm darwin-amd64 darwin-arm64 windows-amd64

darwin-amd64: webui
	GOARCH=amd64 GOOS=darwin $(GOBUILD) -o $(RELEASE_DIR)/$(NAME)-$@
	cp config.yaml.example requirements.txt $(RELEASE_DIR)/

darwin-arm64: webui
	GOARCH=arm64 GOOS=darwin $(GOBUILD) -o $(RELEASE_DIR)/$(NAME)-$@
	cp config.yaml.example requirements.txt $(RELEASE_DIR)/

linux-amd64: webui
	GOARCH=amd64 GOOS=linux $(GOBUILD) -o $(RELEASE_DIR)/$(NAME)-$@
	cp config.yaml.example requirements.txt $(RELEASE_DIR)/

linux-arm64: webui
	GOARCH=arm64 GOOS=linux $(GOBUILD) -o $(RELEASE_DIR)/$(NAME)-$@
	cp config.yaml.example requirements.txt $(RELEASE_DIR)/

linux-arm: webui
	GOARCH=arm GOOS=linux $(GOBUILD) -o $(RELEASE_DIR)/$(NAME)-$@
	cp config.yaml.example requirements.txt $(RELEASE_DIR)/

windows-amd64: webui
	GOOS=windows GOARCH=amd64 $(GOBUILD) -o $(RELEASE_DIR)/$(NAME)-$@.exe
	cp config.yaml.example requirements.txt $(RELEASE_DIR)/
	cp scripts/win/* $(RELEASE_DIR)/

gz_releases=$(addsuffix .gz, $(PLATFORM_LIST))
zip_releases=$(addsuffix .zip, $(WINDOWS_ARCH_LIST))

$(gz_releases): %.gz : %
	chmod +x $(RELEASE_DIR)/$(NAME)-$(basename $@)
	zip -m -j $(RELEASE_DIR)/$(NAME)-$(basename $@)-$(VERSION).zip $(RELEASE_DIR)/$(NAME)-$(basename $@) $(RELEASE_DIR)/config.yaml.example

$(zip_releases): %.zip : %
	zip -m -j $(RELEASE_DIR)/$(NAME)-$(basename $@)-$(VERSION).zip \
		$(RELEASE_DIR)/$(NAME)-$(basename $@).exe \
		$(RELEASE_DIR)/config.yaml.example \
		$(RELEASE_DIR)/requirements.txt \
		$(RELEASE_DIR)/*.bat \
		$(RELEASE_DIR)/*.ps1 \
		$(RELEASE_DIR)/*.vbs


release: webui $(gz_releases) $(zip_releases)

clean:
	rm $(RELEASE_DIR)/*