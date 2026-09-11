BIN := bin/pocp
DIR ?= example
OUT ?= example-clean

.PHONY: all build test vet fmt scan dry-run clean example help
all: fmt vet test

build:
	go build -o $(BIN) .

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -w .

# Отчёт по директивам в DIR (папка или .files файл)
scan:
	go run . $(DIR)

# Предпросмотр очистки DIR без записи на диск
dry-run:
	go run . -clean $(OUT) -dry-run $(DIR)

# Очистить DIR в OUT (папка зеркалится, .files записывает только список файлов)
clean:
	go run . -clean $(OUT) -force $(DIR)

# Очистить example/ в example-clean/ (демо по умолчанию)
example: clean

help:
	@echo "make build | test | vet | fmt | scan | dry-run | clean | example"
	@echo ""
	@echo "DIR  = путь к папке или .files файлу (по умолчанию: example)"
	@echo "OUT  = выходная папка очистки (по умолчанию: example-clean)"
	@echo ""
	@echo "примеры:"
	@echo "  make scan                          # отчёт по директивам в example/"
	@echo "  make scan DIR=example/scr1/src/ahb_tb.files"
	@echo "  make dry-run                       # предпросмотр очистки example/"
	@echo "  make clean                         # очистить example/ -> example-clean/"