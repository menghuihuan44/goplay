# GoPlay developer tasks. On Windows prefer build.ps1; this file is for
# make-compatible shells (WSL/MSYS2/Git Bash).
GO ?= go
BIN := bin
DATA ?= $(CURDIR)/.tmpdata

.PHONY: all build test race run eval audit native clean

all: build test

build:
	$(GO) build ./...
	mkdir -p $(BIN)
	$(GO) build -o $(BIN)/goplayd ./cmd/goplayd
	$(GO) build -o $(BIN)/goplay ./cmd/goplay
	$(GO) build -o $(BIN)/goplay-model ./cmd/goplay-model
	$(GO) build -o $(BIN)/goplay-eval ./cmd/goplay-eval
	$(GO) build -o $(BIN)/goplay-audit ./cmd/goplay-audit

test:
	$(GO) test ./... -count=1

race:
	$(GO) test -race ./internal/engine ./internal/api ./internal/memory ./internal/scheduler -count=1

run:
	$(GO) run ./cmd/goplayd -addr 127.0.0.1:8420 -data $(DATA) -llm offline

eval:
	$(GO) run ./cmd/goplay-eval -quick -json -report-dir reports

audit:
	$(GO) run ./cmd/goplay-audit -json -out reports/audit-report.json

native:
	powershell -NoProfile -ExecutionPolicy Bypass -File native/build.ps1

clean:
	rm -rf $(BIN) .tmpdata .tmp-smoke
