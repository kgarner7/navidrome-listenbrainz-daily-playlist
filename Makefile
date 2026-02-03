PACKAGE := "listenbrainz-daily-playlist"

prod:
	tinygo build -opt=2 -scheduler=none -no-debug -gc=leaking -o plugin.wasm -target wasip1 -buildmode=c-shared .
	zip $(PACKAGE).ndp plugin.wasm manifest.json

dev:
	GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o plugin.wasm plugin.go
	zip $(PACKAGE).ndp plugin.wasm manifest.json

.DEFAULT_GOAL := dev