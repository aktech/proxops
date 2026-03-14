.PHONY: build web dev clean

build: web
	go build -ldflags "-s -w -X main.version=dev -X main.commit=$$(git rev-parse --short HEAD)" -o proxops .

web:
	cd web && npm ci && npm run build

dev:
	cd web && npm run dev

clean:
	rm -rf proxops web/dist web/node_modules
