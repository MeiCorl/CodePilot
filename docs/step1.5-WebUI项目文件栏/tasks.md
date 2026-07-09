# Step 1.5 - WebUI 项目文件栏 - 任务拆分

## Task 1: 后端项目文件模型与安全路径解析

**状态**: 已完成

**目标**: 定义项目文件浏览所需的数据结构、路径校验和文件类型识别逻辑，保证所有访问被限制在 CodePilot 启动目录内。

**影响文件**:
- `src/internal/interaction/web/project_file.go` - 新建，封装路径解析、目录列表、文件读取、类型识别。
- `src/internal/interaction/web/project_file_test.go` - 新建，覆盖安全边界和类型识别。

**依赖**: 无

**具体内容**:
1. 定义项目文件条目结构，包含名称、相对路径、类型、大小、修改时间、是否可预览、语言标识等字段。
2. 定义目录列表结果结构，包含当前相对路径、父目录路径、面包屑、条目列表、是否截断和错误原因。
3. 实现相对路径解析：空路径表示项目根目录；拒绝绝对路径、`..` 越界、空字节等非法输入。
4. 对最终路径执行规范化校验，必要时处理符号链接，确保最终路径仍在 `workdir` 内。
5. 实现文件语言/渲染类型识别：Markdown、JSON、XML、常见代码扩展名、纯文本、二进制。
6. 实现文本/二进制探测，复用标准库内容类型探测思路，允许常见文本、JSON、XML、脚本和源码文件。
7. 增加文件大小限制，超过限制时不读取正文。
8. 单测覆盖：根目录解析、子目录解析、`..` 逃逸、符号链接逃逸、二进制拒绝、大文件拒绝、扩展名识别。

**参考资料**:
- `src/internal/tool/builtin/read_file.go` 的文本/二进制探测方式。
- `src/internal/security/sandbox.go` 的路径边界处理思路。
- `src/internal/interaction/web/file_diff_store.go` 的语言识别风格。

---

## Task 2: WebSocket 协议与 Handler 接入

**状态**: 已完成

**目标**: 新增前端请求项目目录列表和读取文件内容的 WS 协议，并在 Handler 中接入只读业务处理。

**影响文件**:
- `src/internal/interaction/web/protocol.go` - 修改，新增消息类型与 payload 结构。
- `src/internal/interaction/web/handler.go` - 修改，注册并实现新消息处理。
- `src/internal/interaction/web/project_file_test.go` - 修改或补充 handler 级测试。

**依赖**: Task 1

**具体内容**:
1. 新增客户端到服务端消息类型：`list_project_dir`、`read_project_file`。
2. 新增服务端到客户端消息类型：`project_dir`、`project_file`。
3. `list_project_dir` payload 携带相对目录路径；`project_dir` 回包携带目录列表、当前路径、父路径、截断信息和错误原因。
4. `read_project_file` payload 携带相对文件路径；`project_file` 回包携带 found/ok、reason、文件元数据、渲染类型、语言、正文。
5. 在 `Handler.Register` 中注册新消息处理函数。
6. Handler 使用 `h.workdir` 作为项目根；`workdir` 为空时返回明确错误，不 panic。
7. 通过 `sendMessage` 串行写 WS，保持与现有并发写约束一致。
8. 测试覆盖：目录 found、目录不存在、文件 found、二进制文件、越界路径、workdir 为空。

**参考资料**:
- `src/internal/interaction/web/protocol.go` 中 `GetFileDiffPayload` / `FileDiffPayload` 的协议定义模式。
- `src/internal/interaction/web/handler.go` 中 `handleGetFileDiff` 的回包模式。

---

## Task 3: 右侧文件栏 DOM 与布局样式

**状态**: 已完成

**目标**: 在 WebUI 右侧新增项目文件栏，保持现有 UI 风格，并兼容桌面与窄屏。

**影响文件**:
- `src/internal/interaction/web/static/index.html` - 修改，新增右侧文件栏结构与文件预览弹窗容器。
- `src/internal/interaction/web/static/style.css` - 修改，新增三栏布局、文件栏、条目、面包屑、状态和弹窗样式。

**依赖**: 无

**具体内容**:
1. 将现有左侧会话栏 + 主区域布局扩展为左中右三栏，右栏宽度使用稳定 CSS 变量。
2. 右侧文件栏包含标题、刷新按钮、当前路径/面包屑区域、列表区域。
3. 目录项与文件项使用清晰图标或简洁符号区分，目录项可进入，文件项可预览。
4. 增加 loading、empty、error、truncated 状态样式。
5. 新增文件预览弹窗，复用现有 diff/skills modal 的遮罩、Esc、关闭按钮交互风格。
6. 预览弹窗支持标题、文件路径、文件大小、渲染区域和不可预览提示。
7. 窄屏下右侧栏降级为可滚动区域或隐藏式窄栏，不遮挡主对话和输入框。
8. 保证按钮文字和路径文本不溢出父容器，长路径使用省略和 title。

**参考资料**:
- `src/internal/interaction/web/static/index.html` 现有 `sidebar` / `main` / modal 结构。
- `src/internal/interaction/web/static/style.css` 现有布局变量、diff modal、skills modal 样式。

---

## Task 4: 前端文件栏状态、目录导航与 WS 请求

**状态**: 已完成

**目标**: 在 `app.js` 中维护文件栏状态，完成目录列表请求、返回上级、面包屑跳转、刷新和错误处理。

**影响文件**:
- `src/internal/interaction/web/static/app.js` - 修改，新增项目文件栏状态和交互逻辑。

**依赖**: Task 2、Task 3

**具体内容**:
1. 在 DOM 引用和 `MsgType` 常量中加入文件栏相关元素和消息类型。
2. WebSocket 连接成功或收到 `session_loaded` 后请求根目录列表。
3. 实现 `requestProjectDir(path)`，发送 `list_project_dir` 消息并显示 loading。
4. 实现 `onProjectDir(payload)`，渲染目录项、文件项、面包屑、上级入口、空目录和错误状态。
5. 点击目录项请求对应子目录；点击面包屑跳转对应祖先目录；点击刷新重新请求当前目录。
6. 网络断开或回包错误时显示错误状态，并允许用户重试。
7. 对同一目录的快速重复请求做简单序列号或当前路径校验，避免旧回包覆盖新视图。
8. 不影响现有会话列表、slash 命令、工具 diff 弹窗和权限弹窗。

**参考资料**:
- `src/internal/interaction/web/static/app.js` 中 `handleServerMessage` 分发模式。
- `src/internal/interaction/web/static/app.js` 中 `/skills` modal 和 `file_diff` 回包处理模式。

---

## Task 5: 前端文件内容弹窗与多格式渲染

**状态**: 已完成

**目标**: 点击文件后弹窗展示内容，并按 Markdown、JSON、XML、代码或纯文本选择渲染方式；二进制和大文件显示不可预览提示。

**影响文件**:
- `src/internal/interaction/web/static/app.js` - 修改，新增文件读取、弹窗、渲染逻辑。
- `src/internal/interaction/web/static/style.css` - 修改，补充预览内容样式。

**依赖**: Task 2、Task 3

**具体内容**:
1. 实现 `openProjectFileModal(path)`，创建 loading 弹窗并发送 `read_project_file`。
2. 实现 `onProjectFile(payload)`，按 path/request id 将回包路由到当前弹窗。
3. Markdown 使用现有 `renderMarkdown` 或等价的 marked + DOMPurify 链路渲染。
4. JSON 尝试 `JSON.parse` + `JSON.stringify(..., null, 2)` 格式化，再以 `json` 语言高亮；解析失败则按原文高亮或纯文本展示。
5. XML 和代码类文件使用 `hljs.highlight` 或现有 `highlightCode` 辅助函数高亮。
6. 纯文本使用安全转义后的 `<pre>` 展示。
7. `binary`、`too_large`、`not_found`、`not_previewable` 等 reason 显示用户可理解的提示。
8. 弹窗支持 Esc、遮罩、关闭按钮关闭，并在关闭时清理 pending callback。
9. 弹窗内文件内容滚动，不撑破页面布局。

**参考资料**:
- `src/internal/interaction/web/static/app.js` 中 `renderMarkdown`、`highlightCodeInHTML`、`highlightCode`、diff modal 渲染逻辑。

---

## Task 6: 接入主流程

**状态**: 已完成

**目标**: 将项目文件栏能力接入 WebUI 启动和会话加载主流程，保证新功能默认可用且不破坏既有交互。

**影响文件**:
- `src/internal/interaction/web/handler.go` - 修改，确保 handler 注册完整。
- `src/internal/interaction/web/static/app.js` - 修改，确保初始化时机、重连恢复和刷新行为完整。
- `src/internal/interaction/web/static/index.html` - 修改，确保右侧栏在首屏存在。
- `src/internal/interaction/web/static/style.css` - 修改，确保布局最终稳定。

**依赖**: Task 1、Task 2、Task 3、Task 4、Task 5

**具体内容**:
1. 确认 `Handler.Register` 注册所有新增消息类型。
2. 确认 `workdir` 通过现有 `session_loaded` 仍显示在顶部，并作为文件栏根目录来源的服务端依据。
3. 确认 WebSocket 重连或刷新页面后，文件栏自动回到根目录或恢复当前目录。
4. 确认右侧栏不会影响输入区高度、流式输出滚动和左侧会话列表。
5. 确认所有前端新函数均挂在现有闭包内，不污染全局 API。
6. 保持旧浏览器/HTTP 环境下的降级行为：无法访问 Clipboard 等能力不影响文件栏。

**参考资料**:
- `src/internal/interaction/web/static/app.js` 初始化和 `session_loaded` 处理逻辑。
- `src/internal/interaction/web/handler.go` 注册模式。

---

## Task 7: 端到端验证

**状态**: 已完成

**目标**: 覆盖项目文件栏核心链路、异常链路和视觉/交互回归，并同步更新 checklist。

**影响文件**:
- `docs/step1.5-WebUI项目文件栏/checklist.md` - 修改，填写实际验证结果。
- `src/internal/interaction/web/project_file_test.go` - 新建或补充。
- 可能新增 `src/internal/interaction/web/project_file_e2e_test.go` - 覆盖 WS 协议级链路。

**依赖**: Task 1 到 Task 6

**具体内容**:
1. 运行 Web 包相关单测，覆盖目录列表、文件读取、路径逃逸、二进制拒绝、大文件拒绝。
2. 运行全项目 `go test ./...`。
3. 启动 WebUI，验证右侧文件栏首屏显示当前项目根目录内容。
4. 点击目录进入子目录，点击上级和面包屑返回。
5. 点击 Markdown、JSON、XML、Go/JS/CSS 等文件，确认弹窗渲染和高亮符合预期。
6. 点击二进制文件，确认显示不可预览提示。
7. 验证大文件不会导致页面卡顿，并显示文件过大提示。
8. 验证路径越界请求被拒绝，前端显示错误而不崩溃。
9. 验证现有会话、工具 diff、权限确认、/skills、/sessions 交互不回归。
10. 将 checklist 中每一项的实际结果和结论补齐。

**参考资料**:
- `docs/step1.4-WebUI工具展示优化/checklist.md` 的验证记录格式。
