# Step 1.6 - WebUI 项目侧栏多标签 - 任务拆分

## Task 1: 后端 Git 变更模型与只读差异读取

**状态**: 已完成

**目标**: 为 WebUI 提供当前项目 Git 变更文件列表与单文件左右栏内容读取能力。

**影响文件**:
- `src/internal/interaction/web/project_git.go` - 新建，封装 Git 仓库识别、变更列表、单文件左右内容读取和错误原因。
- `src/internal/interaction/web/project_git_test.go` - 新建，覆盖仓库状态、变更状态和异常场景。

**依赖**: 无

**具体内容**:
1. 定义 Git 变更文件条目、变更列表结果、变更内容结果等后端模型。
2. 在项目工作目录内识别 Git 仓库，非仓库或 Git 命令不可用时返回稳定原因。
3. 只读取当前项目内的变更文件，拒绝越界路径、空字节和绝对路径。
4. 覆盖常见变更状态：修改、新增、删除、重命名、未跟踪，以及组合状态的可读展示。
5. 为单个变更文件读取变更前与变更后内容，支持新增、删除、未跟踪等左右栏空侧场景。
6. 对二进制文件、大文件和读取失败提供稳定 reason，不读取正文。
7. 单测使用临时目录初始化 Git 仓库或伪造命令依赖，覆盖成功和降级分支。

**参考资料**:
- `src/internal/interaction/web/project_file.go` 的路径解析、二进制探测和大小限制思路。
- `src/internal/interaction/web/file_diff_store.go` 的左右栏内容模型。
- `src/internal/interaction/web/handler.go` 现有只读 Handler 模式。

---

## Task 2: 后端项目内容搜索模型与扫描逻辑

**状态**: 已完成

**目标**: 提供受项目边界保护的内容搜索能力，支持关键词、路径限定、正则和排除规则。

**影响文件**:
- `src/internal/interaction/web/project_search.go` - 新建，封装搜索请求、搜索结果、路径过滤、正则匹配和结果限制。
- `src/internal/interaction/web/project_search_test.go` - 新建，覆盖关键词、路径、正则、排除规则和边界保护。

**依赖**: 无

**具体内容**:
1. 定义搜索请求、命中结果、命中片段和搜索限制信息。
2. 支持普通关键词搜索，返回文件路径、命中行号、片段和命中数量。
3. 支持限定相对路径，只在指定目录或文件范围内搜索。
4. 支持正则匹配，正则非法时返回稳定 reason。
5. 支持排除规则，允许用户排除特定文件、目录或 glob 模式。
6. 跳过二进制文件、过大文件、常见构建产物目录和越界路径。
7. 对最大扫描文件数、最大命中数和最大片段长度做限制，并在结果中标记截断。
8. 单测覆盖空关键词、普通搜索、正则搜索、路径限定、排除规则、大文件跳过和越界拒绝。

**参考资料**:
- `src/internal/interaction/web/project_file.go` 的文本识别与安全路径处理。
- `src/internal/tool/builtin/grep.go` 的搜索语义参考。

---

## Task 3: WebSocket 协议与 Handler 接入

**状态**: 已完成

**目标**: 扩展 WebUI 协议，使前端可以请求 Git 变更列表、Git 文件差异和项目内容搜索。

**影响文件**:
- `src/internal/interaction/web/protocol.go` - 修改，新增 Git 与搜索消息类型和 payload。
- `src/internal/interaction/web/handler.go` - 修改，注册并实现新增消息处理函数。
- `src/internal/interaction/web/project_git_test.go` - 修改或补充 Handler 级覆盖。
- `src/internal/interaction/web/project_search_test.go` - 修改或补充 Handler 级覆盖。

**依赖**: Task 1, Task 2

**具体内容**:
1. 新增客户端到服务端消息：请求 Git 变更列表、请求 Git 文件差异、请求项目搜索。
2. 新增服务端到客户端消息：Git 变更列表、Git 文件差异、项目搜索结果。
3. 所有请求带可选 request id，便于前端过滤过期响应。
4. Handler 使用当前 `workdir` 构造只读服务，`workdir` 为空时稳定返回错误。
5. Handler 将后端错误映射为结构化 payload，不通过 panic 或非结构化文本传递。
6. 复用 `sendMessage` 发送响应，保持 WebSocket 写入约束一致。
7. 测试覆盖正常请求、非法 payload、非仓库、非法正则、越界路径和空 workdir。

**参考资料**:
- `src/internal/interaction/web/protocol.go` 中 `ListProjectDirPayload` / `ProjectFilePayload` 的定义模式。
- `src/internal/interaction/web/handler.go` 中 `handleListProjectDir` / `handleReadProjectFile` 的实现模式。

---

## Task 4: 项目侧栏多标签 DOM 与样式

**状态**: 已完成

**目标**: 将右侧项目文件栏升级为文件、Git、搜索三标签侧栏，并保持现有视觉风格与响应式布局。

**影响文件**:
- `src/internal/interaction/web/static/index.html` - 修改，新增项目侧栏 tab、Git 面板、搜索面板和必要状态容器。
- `src/internal/interaction/web/static/style.css` - 修改，新增 tab、Git 列表、搜索表单、搜索结果和双栏内容复用样式。

**依赖**: 无

**具体内容**:
1. 在项目侧栏顶部新增三标签切换控件：文件、Git、搜索。
2. 保留现有文件列表 DOM，作为文件 tab 内容。
3. 新增 Git tab 的刷新按钮、状态计数、变更文件列表、加载/空/错误状态。
4. 新增搜索 tab 的关键词输入、路径输入、正则开关、排除规则输入、执行按钮和结果列表。
5. 新增或复用左右栏内容弹窗样式，用于 Git 文件差异展示。
6. 窄屏下侧栏 tab、搜索表单和结果列表不遮挡主对话与输入区。
7. 长路径、长文件名、长搜索片段都必须可换行或省略，不撑破容器。

**参考资料**:
- `src/internal/interaction/web/static/index.html` 现有 `project-file-panel` 结构。
- `src/internal/interaction/web/static/style.css` 现有项目文件栏、diff modal、skills tabs 样式。

---

## Task 5: 前端 Git 与搜索交互逻辑

**状态**: 已完成

**目标**: 在前端维护项目侧栏多 tab 状态，完成 Git 刷新、Git diff 打开、搜索请求和结果渲染。

**影响文件**:
- `src/internal/interaction/web/static/app.js` - 修改，新增多 tab 状态、Git 请求/回包处理、搜索请求/回包处理和左右栏差异展示逻辑。

**依赖**: Task 3, Task 4

**具体内容**:
1. 扩展 DOM 引用、消息类型常量和初始化绑定。
2. 实现项目侧栏 tab 切换；文件 tab 保留现有加载行为，Git / 搜索 tab 按需加载。
3. Git tab 支持刷新变更列表，渲染状态、路径、文件大小或状态说明。
4. 点击 Git 变更文件后请求单文件差异，并以左右栏展示变更前后内容。
5. 搜索 tab 支持输入关键词、路径、正则开关、排除规则，并发起搜索请求。
6. 搜索结果按文件分组或按命中列表展示，包含路径、行号和片段。
7. 点击搜索结果后打开现有文件预览弹窗或定位到对应文件内容。
8. 对 Git 与搜索请求使用 request id 或序列号过滤旧回包。
9. 断线、重连、非法输入和服务端错误均显示可恢复状态，不影响现有文件 tab。

**参考资料**:
- `src/internal/interaction/web/static/app.js` 中项目文件栏状态与回包过滤逻辑。
- `src/internal/interaction/web/static/app.js` 中 `openFileDiffModal` 与 `renderDiffGrid` 的左右栏展示逻辑。

---

## Task 6: 接入主流程

**状态**: 已完成

**目标**: 将项目侧栏多标签能力接入 WebUI 默认启动、会话加载和重连流程。

**影响文件**:
- `src/internal/interaction/web/handler.go` - 修改，确认新增 handler 注册完整。
- `src/internal/interaction/web/static/app.js` - 修改，确认初始化、重连和 tab 懒加载流程完整。
- `src/internal/interaction/web/static/index.html` - 修改，确认首屏 DOM 完整。
- `src/internal/interaction/web/static/style.css` - 修改，确认布局最终稳定。

**依赖**: Task 1, Task 2, Task 3, Task 4, Task 5

**具体内容**:
1. 确认 WebSocket 建立后文件 tab 仍自动加载项目根目录。
2. 确认 Git tab 首次打开或刷新时加载当前变更列表。
3. 确认搜索 tab 不在空关键词时自动扫描项目。
4. 确认断线后清理 pending 状态，重连后用户可继续刷新或搜索。
5. 确认现有会话列表、输入发送、工具 diff、权限确认、/skills、/sessions 不回归。
6. 确认新增逻辑只在 WebUI 交互层内扩展，不影响工具层与引擎层。

**参考资料**:
- `src/internal/interaction/web/static/app.js` 的 `init`、WebSocket open/close、`session_loaded` 处理逻辑。
- `src/internal/interaction/web/handler.go` 的 `Register` 注册模式。

---

## Task 7: 端到端验证

**状态**: 已完成（自动化验证已执行；浏览器人工 E2E 待确认）

**目标**: 覆盖项目侧栏文件、Git、搜索三类核心链路与异常链路，并同步 checklist。

**影响文件**:
- `docs/step1.6-WebUI项目侧栏多标签/checklist.md` - 修改，填写实际验证结果。
- `src/internal/interaction/web/project_git_test.go` - 修改或补充。
- `src/internal/interaction/web/project_search_test.go` - 修改或补充。
- 可能新增 `src/internal/interaction/web/project_sidebar_e2e_test.go` - 覆盖协议级链路。

**依赖**: Task 1, Task 2, Task 3, Task 4, Task 5, Task 6

**具体内容**:
1. 运行 Web 包相关单测，覆盖 Git、搜索、项目文件栏兼容性。
2. 运行全项目 `go test ./...`。
3. 启动 WebUI，验证右侧侧栏显示文件、Git、搜索三个 tab。
4. 验证文件 tab 仍可浏览目录、打开文件预览。
5. 在 Git 仓库中制造修改、新增、删除或未跟踪文件，验证 Git tab 列表与左右栏差异展示。
6. 验证非 Git 仓库或 Git 不可用时 Git tab 显示错误而不崩溃。
7. 执行普通关键词搜索、路径限定搜索、正则搜索和排除规则搜索，验证结果准确。
8. 验证非法正则、空关键词、二进制文件、大文件和结果截断的展示。
9. 验证现有工具 diff、权限确认、会话切换和 slash 命令交互不回归。
10. 将 checklist 中每一项的实际结果和结论补齐。

**参考资料**:
- `docs/step1.5-WebUI项目文件栏/checklist.md` 的验证记录格式。
