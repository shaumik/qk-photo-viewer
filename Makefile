# QK — burst photo culler

.PHONY: ui test dev build

## Open the UI with the mock backend (no Go needed): http://localhost:5173
ui:
	cd frontend && python3 -m http.server 5173

## Run Go tests (pure packages; no webview deps needed)
test:
	go test ./internal/...

## Live-reload desktop app (needs macOS + the Wails CLI:
##   go install github.com/wailsapp/wails/v2/cmd/wails@latest)
dev:
	wails dev

## Package the .app (macOS)
build:
	wails build
