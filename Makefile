# profkit - Go 服务性能采集与分析工具箱
# 用法:
#   make build          编译（前置: 全部扫描检查）
#   make test           全量测试
#   make serve ARGS="-l :8080"   本地起 Web 服务
#   make install        安装到 $GOBIN

# 依赖要求 go >= 1.26（见 go.mod），本机 toolchain 较旧时自动经 module proxy 拉取
# 直接赋值而非 ?=：环境里常有 GOTOOLCHAIN=local，?= 会被它抢先（命令行 make GOTOOLCHAIN=xxx 仍可覆盖）
GOTOOLCHAIN = auto
export GOTOOLCHAIN

# 工具路径（允许外部覆盖; 未安装时对应目标自动跳过）
GO ?= go
GOFMT ?= gofmt
GOLINT ?= golangci-lint
# 扫描时是否自动修复（默认 true: gofmt -w；false 时只检查）
FIX ?= true
# 构建产物
BIN ?= profkit
VERSION ?= 0.1.0

# fetch 默认参数（make fetch TARGET=... 可覆盖）
TARGET ?= 127.0.0.1:8091
KIND ?= cpu
D ?= 30
UI ?= true          # 采集完拉起官方 pprof WebUI（阻塞至 Ctrl-C，浏览器由 pprof 自动打开）
OPEN ?= false
REPORTS ?= reports
OPENFLAG = $(if $(filter true,$(OPEN)),,-open=false)
UIFLAG = $(if $(filter true,$(UI)),-ui,)

# ============================================================================
# 代码扫描检查（build 前置依赖）
# ============================================================================

# gofmt: 检查代码格式（FIX=true 时自动修复）
.PHONY: fmt
fmt:
ifeq ($(FIX),true)
	@echo ">> [fmt] 格式化代码 (gofmt -s -w)"
	@$(GOFMT) -s -w .
else
	@echo ">> [fmt] 检查代码格式 (gofmt -l)"
	@files=$$($(GOFMT) -s -l . 2>/dev/null); \
	if [ -n "$$files" ]; then \
		echo "以下文件未通过 gofmt 检查 (运行 make fmt 修复):"; \
		echo "$$files"; \
		exit 1; \
	fi
endif

# go vet: 编译器级静态检查
.PHONY: vet
vet:
	@echo ">> [vet] go vet 静态检查"
	@$(GO) vet ./...

# golangci-lint: 综合静态分析。未安装或其构建版本低于 go.mod 目标版本时跳过而非失败；
# 代码本身的 lint 错误仍然置败
.PHONY: lint
lint:
	@command -v $(GOLINT) >/dev/null 2>&1 || { \
		echo ">> [lint] 跳过: $(GOLINT) 未安装 (安装: https://golangci-lint.run/usage/install/)"; \
		exit 0; \
	}
	@echo ">> [lint] golangci-lint 综合检查"
	@if ! $(GOLINT) run ./... 2>/tmp/profkit-lint.err; then \
		if grep -q "lower than the targeted Go version" /tmp/profkit-lint.err; then \
			echo ">> [lint] 跳过: golangci-lint 构建版本低于 go.mod 目标版本 (升级 golangci-lint 可启用)"; \
			exit 0; \
		fi; \
		cat /tmp/profkit-lint.err >&2; exit 1; \
	fi; rm -f /tmp/profkit-lint.err

# go mod: 检查依赖是否整洁（无未使用/缺失依赖）
.PHONY: mod-tidy-check
mod-tidy-check:
	@echo ">> [mod] 检查 go.mod/go.sum 是否整洁"
	@cp go.mod go.mod.bak; [ -f go.sum ] && cp go.sum go.sum.bak || true; \
	$(GO) mod tidy; \
	if ! diff -q go.mod go.mod.bak >/dev/null; then \
		echo "go.mod 不整洁, 请运行 'go mod tidy' 后提交"; \
		mv go.mod.bak go.mod; [ -f go.sum.bak ] && mv go.sum.bak go.sum || true; \
		exit 1; \
	fi; \
	if [ -f go.sum.bak ] && ! diff -q go.sum go.sum.bak >/dev/null; then \
		echo "go.sum 不整洁, 请运行 'go mod tidy' 后提交"; \
		mv go.mod.bak go.mod; mv go.sum.bak go.sum; \
		exit 1; \
	fi; \
	rm -f go.mod.bak go.sum.bak

# check: 聚合所有扫描检查（build 的前置依赖）
.PHONY: check
check: fmt vet lint mod-tidy-check
	@echo ">> [check] 所有扫描检查通过"

# ============================================================================
# 编译 / 测试 / 运行
# ============================================================================

# 构建二进制（版本号经 ldflags 注入, 末尾附构建时间戳）
.PHONY: build
build: check
	@echo ">> [build] 编译 $(BIN) $(VERSION)"
	@$(GO) build -ldflags "-X main.version=$(VERSION)-$$(date +%Y%m%d%H%M%S)" -o $(BIN) .

# 全量测试
.PHONY: test
test:
	@$(GO) test -timeout 120s ./...

# 快速测试（跳过 -short 标记的用例）
.PHONY: test-short
test-short:
	@$(GO) test -short -timeout 60s ./...

# 本地运行: make serve / make mcp（ARGS 透传, 如 ARGS="-l :9090"）
.PHONY: serve mcp
serve:
	@$(GO) run . serve $(ARGS)
mcp:
	@$(GO) run . mcp $(ARGS)

# 一次性采集（默认拉起官方 pprof WebUI；UI=false 则只生成 HTML 报告）
# 用法: make fetch [TARGET=127.0.0.1:8091] [KIND=cpu] [D=5] [UI=false] [OPEN=true] [EXTRA="-t /tmp/x"]
.PHONY: fetch
fetch:
	@$(GO) run . fetch $(UIFLAG) $(OPENFLAG) -o $(REPORTS)/profkit-report-$(KIND)-$(shell date +%Y%m%d%H%M%S).html -kind $(KIND) -d $(D) $(TARGET) $(EXTRA)

# 安装到 $GOBIN（或 $GOPATH/bin），之后可直接 profkit ... 调用
.PHONY: install
install:
	@$(GO) install -ldflags "-X main.version=$(VERSION)-$$(date +%Y%m%d%H%M%S)" .

.PHONY: clean
clean:
	@rm -rf $(BIN)

.PHONY: all
all: build
