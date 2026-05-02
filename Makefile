VERSION ?= 1.0.0
OUTPUT_DIR = dist
LDFLAGS = -ldflags "-s -w -X main.version=$(VERSION)"

.PHONY: all windows linux android deb clean run

all: windows linux android

windows:
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build $(LDFLAGS) -o $(OUTPUT_DIR)/fasentneo-windows-amd64.exe .

linux:
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build $(LDFLAGS) -o $(OUTPUT_DIR)/fasentneo-linux-amd64 .
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build $(LDFLAGS) -o $(OUTPUT_DIR)/fasentneo-linux-arm64 .

android:
	GOOS=android GOARCH=arm64 CGO_ENABLED=0 go build $(LDFLAGS) -o $(OUTPUT_DIR)/fasentneo-android-arm64 .
	@echo "NOTE: Run on Android via Termux or wrap in an APK with gomobile/WebView"

deb: linux
	mkdir -p $(OUTPUT_DIR)/fasentneo_$(VERSION)_amd64/DEBIAN
	mkdir -p $(OUTPUT_DIR)/fasentneo_$(VERSION)_amd64/usr/local/bin
	mkdir -p $(OUTPUT_DIR)/fasentneo_$(VERSION)_amd64/usr/share/applications
	cp $(OUTPUT_DIR)/fasentneo-linux-amd64 $(OUTPUT_DIR)/fasentneo_$(VERSION)_amd64/usr/local/bin/fasentneo
	echo "Package: fasentneo\nVersion: $(VERSION)\nSection: net\nPriority: optional\nArchitecture: amd64\nMaintainer: FasentNeo Team\nDescription: Fast cross-platform file transfer tool" > $(OUTPUT_DIR)/fasentneo_$(VERSION)_amd64/DEBIAN/control
	echo "[Desktop Entry]\nName=FasentNeo\nComment=Fast File Transfer\nExec=fasentneo\nTerminal=false\nType=Application\nCategories=Utility;Network;" > $(OUTPUT_DIR)/fasentneo_$(VERSION)_amd64/usr/share/applications/fasentneo.desktop
	@echo "Run: dpkg-deb --build $(OUTPUT_DIR)/fasentneo_$(VERSION)_amd64"

clean:
	rm -rf $(OUTPUT_DIR)

run:
	go run .

vet:
	go vet ./...
