PLUGIN_ID := codex-invite
DIST_DIR := dist

UNAME_S := $(shell uname -s)

ifeq ($(OS),Windows_NT)
PLUGIN_EXT := dll
else ifeq ($(UNAME_S),Darwin)
PLUGIN_EXT := dylib
else
PLUGIN_EXT := so
endif

.PHONY: build test clean package

build:
	mkdir -p $(DIST_DIR)
	go build -buildmode=c-shared -o $(DIST_DIR)/$(PLUGIN_ID).$(PLUGIN_EXT) .
	rm -f $(DIST_DIR)/$(PLUGIN_ID).h

test:
	go test ./...

clean:
	rm -rf $(DIST_DIR)

package: build
	cd $(DIST_DIR) && zip -q $(PLUGIN_ID)_0.1.0_$$(go env GOOS)_$$(go env GOARCH).zip $(PLUGIN_ID).$(PLUGIN_EXT)
	cd $(DIST_DIR) && shasum -a 256 $(PLUGIN_ID)_0.1.0_$$(go env GOOS)_$$(go env GOARCH).zip > checksums.txt
