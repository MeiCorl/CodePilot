# CodePilot 构建入口
# ----------------------------------------------------------------------
# 把图标资源生成 → .syso 资源注入 → Go 编译 三个步骤固化下来，
# 让任何协作者执行一条 `make build` 即可获得带 LOGO 图标的 exe。
#
# 前置依赖：
#   - Go >= 1.20
#   - Python >= 3.8 + pillow
#   - akavel/rsrc（go install github.com/akavel/rsrc@latest）
# ----------------------------------------------------------------------

APP        := CodePilot
BUILD_DIR  := build
ASSETS_DIR := $(BUILD_DIR)/assets
DIST_DIR   := $(BUILD_DIR)/dist
SYSO_NAME  := resource_windows_amd64.syso
ICO        := $(ASSETS_DIR)/icon.ico
RC         := $(ASSETS_DIR)/icon.rc
SYSO       := src/$(SYSO_NAME)
ENTRY      := ./src
RSRC       := $(shell go env GOPATH)/bin/rsrc.exe
PYTHON     := python

.PHONY: help build icon clean run-test-icon dist

help:
	@echo "CodePilot 构建命令："
	@echo "  make build        - 完整构建 Windows exe（含图标，默认目标）"
	@echo "  make icon         - 重新生成图标资源（icon.ico / icon.png / icon.rc）"
	@echo "  make clean        - 清理 syso 与构建产物"
	@echo "  make dist         - 构建并归档到 build/dist/"

# 默认目标：图标 + syso + 编译
build: icon $(SYSO)
	@mkdir -p $(DIST_DIR)
	@echo ">> 编译 $(APP).exe ..."
	@GOOS=windows GOARCH=amd64 go build -ldflags="-s -w" -o $(DIST_DIR)/$(APP).exe $(ENTRY)
	@echo ">> 产物: $(DIST_DIR)/$(APP).exe"
	@ls -lh $(DIST_DIR)/$(APP).exe

# 1. 生成 ICO / PNG / RC（与 WebUI favicon 同款 LOGO）
icon:
	@echo ">> 生成图标资源 ..."
	@$(PYTHON) $(ASSETS_DIR)/generate_icon.py

# 2. 把 .ico 编译成 Go 工具链能识别的 .syso
#    必须放在 main 包同目录，GOOS=windows GOARCH=amd64 时自动链接
$(SYSO): $(ICO) $(RC)
	@echo ">> 编译 .syso (akavel/rsrc) ..."
	@if [ ! -f "$(RSRC)" ]; then \
		echo "!! 未找到 rsrc，正在安装 akavel/rsrc ..."; \
		go install github.com/akavel/rsrc@latest; \
	fi
	@$(RSRC) -ico $(ICO) -o $(SYSO)

dist: build
	@echo ">> 归档完成: $(DIST_DIR)/$(APP).exe"

clean:
	@echo ">> 清理 ..."
	@rm -f $(SYSO) $(DIST_DIR)/$(APP).exe
