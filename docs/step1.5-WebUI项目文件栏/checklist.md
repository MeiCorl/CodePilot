# Step 1.5 - WebUI 项目文件栏 - 验收清单

## 后端能力

- [x] 目录列表仅访问项目根目录内路径
  - 预期: 空路径和合法相对路径可访问；`..`、绝对路径伪造和空字节输入被拒绝。
  - 实际: `go test ./src/internal/interaction/web` 覆盖根目录、子目录、绝对路径、`..` 越界和空字节输入；非法输入均返回拒绝 reason。
  - 结论: 通过

- [x] 符号链接不能逃逸项目根目录
  - 预期: 指向项目外的符号链接目录不可进入，文件不可读取，并返回明确错误 reason。
  - 实际: `go test ./src/internal/interaction/web` 覆盖项目内 symlink 目录可展示并进入；指向项目外的目录/文件 symlink，`ListDir` 和 `ReadFile` 均返回 `outside_workdir`。
  - 结论: 通过

- [x] 当前层级目录列表稳定排序
  - 预期: 目录在前、文件在后，同类按名称升序；隐藏文件和普通文件均按同一规则展示。
  - 实际: `go test ./src/internal/interaction/web` 覆盖目录优先、文件随后、同类名称升序的当前层列表排序。
  - 结论: 通过

- [x] 超大目录不会拖垮 UI
  - 预期: 目录条目超过上限时回包标记 truncated，前端显示截断提示。
  - 实际: `go test ./src/internal/interaction/web` 使用自定义条目上限验证超限时只返回上限内条目，并标记 `truncated=true`、reason=`entry_limit`。
  - 结论: 通过

- [x] 文本文件可读取并返回渲染类型
  - 预期: `.md`、`.json`、`.xml`、`.go`、`.js`、`.ts`、`.css`、`.yaml`、`.sh`、`.py`、`.sql` 等返回可预览正文和语言标识。
  - 实际: `go test ./src/internal/interaction/web` 覆盖 Markdown 文本读取和 markdown/json/xml/go/js/ts/css/yaml/sh/python/sql/plain 渲染类型与 highlight.js 语言标识。
  - 结论: 通过

- [x] 二进制文件不返回正文
  - 预期: 二进制文件回包为不可预览状态，正文为空，reason 可供前端显示。
  - 实际: `go test ./src/internal/interaction/web` 覆盖含 NUL 字节的二进制文件，返回 `ok=false`、reason=`binary`、正文为空。
  - 结论: 通过

- [x] 大文件不返回完整正文
  - 预期: 超过大小限制的文件回包为 too_large，正文为空，包含文件大小信息。
  - 实际: `go test ./src/internal/interaction/web` 使用自定义文件大小上限验证超限文件返回 `too_large`、正文为空，并保留文件大小元数据。
  - 结论: 通过

- [x] 新增 WS 协议 found / error 分支完整
  - 预期: `list_project_dir` 和 `read_project_file` 对成功、不存在、非法路径、不可预览均有稳定回包。
  - 实际: `go test ./src/internal/interaction/web` 通过；覆盖 `project_dir` 目录成功、not_found、outside_workdir、empty_workdir，以及 `project_file` 文件成功、binary、outside_workdir、empty_workdir 稳定回包。
  - 结论: 通过

## 前端能力

- [x] 右侧文件栏 DOM 与预览弹窗静态结构完整
  - 预期: `#app` 内存在与 `.sidebar`、`.main` 同级的右侧文件栏，包含标题、刷新按钮、当前路径、面包屑、列表、loading/empty/error/truncated 状态容器；页面内存在默认隐藏的文件预览弹窗容器。
  - 实际: `index.html` 新增 `#project-file-panel` 和 `#project-file-modal`，静态包含上述结构；`go test ./src/internal/interaction/web` 通过，embed 静态资源可编译。
  - 结论: 通过

- [x] 右侧文件栏首屏展示项目根目录
  - 预期: WebUI 建立连接后，右侧栏自动显示 CodePilot 启动路径下的目录和文件。
  - 实际: `app.js` 在 WebSocket 连接成功后强制刷新根目录；`session_loaded` 仅在当前未处于已加载根目录时补拉，避免同一连接重复请求；`project_dir` 回包会渲染当前路径、面包屑、count、截断提示和目录/文件列表。`node --check src/internal/interaction/web/static/app.js` 与 `go test ./src/internal/interaction/web` 通过。
  - 结论: 通过

- [x] 点击目录进入子目录
  - 预期: 点击目录项后列表切换到该目录内容，当前路径和面包屑同步更新。
  - 实际: 目录条目由 `buildProjectEntryItem` 渲染为可点击按钮，点击后调用 `requestProjectDir(entry.path)`；`onProjectDir` 只接受 pending path 匹配的回包并更新列表、当前路径和面包屑。静态 grep 已确认 `requestProjectDir` / `onProjectDir` 链路存在。
  - 结论: 通过

- [x] 支持返回上级目录
  - 预期: 非根目录下出现上级入口，点击后返回父目录。
  - 实际: `renderProjectFileList` 在非根目录顶部插入 `..` 上级入口，点击后用回包 `parent_path` 调用 `requestProjectDir(parentPath || '')` 返回父目录或根目录。
  - 结论: 通过

- [x] 支持面包屑跳转
  - 预期: 点击面包屑任一层级后跳转到对应目录。
  - 实际: `renderProjectPathbar` 使用后端 `breadcrumbs` 渲染每层按钮，点击任一层会调用 `requestProjectDir(crumb.path)`；当前层设置 `is-active`，根路径回退显示为 `root`。
  - 结论: 通过

- [x] 支持刷新当前目录
  - 预期: 点击刷新按钮重新拉取当前目录，loading 和错误状态显示正常。
  - 实际: `bindProjectFilePanel` 通过 `projectPanelBound` 保证只绑定一次事件；刷新按钮绑定到 `requestProjectDir(state.projectDirPath || '', { force: true })`，请求期间显示 loading 并禁用刷新按钮，发送失败或 `ok=false` 回包时展示错误文案并恢复按钮可用。
  - 结论: 通过
- [x] 目录加载错误状态可观察且可重试
  - 预期: 目录请求失败、后端返回错误或 WebSocket 断开时，右侧栏显示明确错误状态，用户可在恢复连接后重试刷新。
  - 实际: `onProjectDir(ok=false)` 通过 reason 映射展示错误文案；`sendWS` 失败和 WebSocket 断开会显示错误状态，刷新按钮恢复可用；目录请求携带并回显 `request_id`，结合 pending path 校验忽略旧目录回包，避免旧错误覆盖当前视图。
  - 结论: 通过

- [x] Markdown 文件按富文本渲染
  - 预期: 标题、列表、代码块等 Markdown 内容被渲染，代码块有高亮，内容经过 XSS 过滤。
  - 实际: Task 5 在 `onProjectFile` 成功回包后按 `render_type=markdown` 调用现有 `renderMarkdown`，并对弹窗内代码块执行 `enhanceCodeBlocks`，复用 DOMPurify 与 highlight.js 链路；`node --check src/internal/interaction/web/static/app.js` 与 `go test ./src/internal/interaction/web` 通过。
  - 结论: 通过

- [x] JSON 文件格式化和高亮
  - 预期: 合法 JSON 以缩进格式展示并高亮；非法 JSON 不崩溃，回退为文本/代码展示。
  - 实际: Task 5 对 `render_type=json` 执行 `JSON.parse` + `JSON.stringify(..., null, 2)` 后用 `highlightCode(..., json)` 展示；解析失败时显示简短提示并按原文高亮展示；`node --check src/internal/interaction/web/static/app.js` 与 `go test ./src/internal/interaction/web` 通过。
  - 结论: 通过

- [x] XML 和代码文件高亮
  - 预期: `.xml`、`.go`、`.js`、`.ts`、`.css`、`.yaml`、`.sh`、`.py`、`.sql` 等使用 highlight.js 展示。
  - 实际: Task 5 对 `render_type=xml` / `code` 使用后端返回的 `language` 调用 `highlightCode`，以 `<pre class="project-file-preview-code hljs">` 渲染；`node --check src/internal/interaction/web/static/app.js` 与 `go test ./src/internal/interaction/web` 通过。
  - 结论: 通过

- [x] 普通文本文件安全展示
  - 预期: `.txt` 或未知文本扩展名以转义后的 `<pre>` 展示，不执行 HTML/脚本。
  - 实际: Task 5 对 `plain` 或未知渲染类型使用 `textContent` 写入 `<pre class="project-file-preview-plain">`，不执行 HTML；`node --check src/internal/interaction/web/static/app.js` 与 `go test ./src/internal/interaction/web` 通过。
  - 结论: 通过

- [x] 二进制文件显示不可预览提示
  - 预期: 点击二进制文件弹窗显示不可预览原因，不展示乱码。
  - 实际: Task 5 对 `binary`、`too_large`、`not_previewable`、`not_found`、`outside_workdir`、`read_error`、`is_directory` 等失败 reason 映射为中文状态文案，仅展示不可预览/错误提示，不展示正文；`node --check src/internal/interaction/web/static/app.js` 与 `go test ./src/internal/interaction/web` 通过。
  - 结论: 通过

- [x] 文件预览弹窗关闭方式完整
  - 预期: Esc、遮罩点击、关闭按钮均能关闭弹窗，并清理 pending 回调。
  - 实际: `closeProjectFileModal` 会清理 `projectFilePending` 和超时定时器；关闭按钮与遮罩通过 `data-project-file-modal-close` 绑定，Esc 仍触发关闭；WebSocket 断开时 `handleProjectFileConnectionClosed` 会立即清理 pending 并在已打开弹窗内显示重试提示。
  - 结论: 通过

- [x] 主流程加载与旧回包过滤稳定
  - 预期: 初始化、WebSocket 打开、会话加载/切换、断线重连后文件栏能合理回到项目根目录，且旧目录/文件回包不会覆盖当前状态。
  - 实际: `onWSOpen` 强制拉取根目录，`session_loaded` 使用 `skipIfCurrent` 避免重复补拉；同一路径 pending 请求会直接复用，刷新按钮显式 `force`；`list_project_dir` / `read_project_file` 请求携带 `request_id` 并由 handler 回显，前端按 `request_id` 与 path 双重过滤旧回包；关闭或断线会清理文件预览 pending。
  - 结论: 通过

- [x] 三栏布局不遮挡主流程
  - 预期: 右侧文件栏不影响主对话滚动、流式输出、输入框和左侧会话列表。
  - 实际: `style.css` 将 `#app` 扩展为 `sidebar/main/project` 三栏 grid，主区使用 `minmax(0, 1fr)` 保持滚动与输入区独立；窄屏下右侧文件栏降级为底部横向滚动区，620px 以下隐藏左侧会话栏并保留主对话与输入区。
  - 结论: 通过

## 兼容性与回归

- [x] 现有工具 diff 弹窗不回归
  - 预期: WriteFile/EditFile 的“查看改动”按钮和双栏 diff 弹窗仍可使用。
  - 实际: Task 4 仅在消息分发中新增 `project_dir` / `project_file` 分支，原 `file_diff` 分支和 `_fileDiffCallbacks` 逻辑未改动；`node --check` 与 `go test ./src/internal/interaction/web` 通过。
  - 结论: 通过

- [x] 现有 slash 命令不回归
  - 预期: `/sessions`、`/skills`、`/clear`、`/compact` 等命令行为不受影响。
  - 实际: Task 4 未修改 slash 命令清单、输入分发或 `commandTypeByName` 构造逻辑；新增目录消息类型独立于 slash 命令路径。`node --check` 与 `go test ./src/internal/interaction/web` 通过。
  - 结论: 通过

- [x] 权限确认弹窗不回归
  - 预期: 触发权限确认时，权限弹窗仍可正常显示、授权和拒绝。
  - 实际: Task 4 未修改 `permission_request` / `permission_response` / `permission_mode` 分支和权限弹窗函数；`project_file` 预留分支为空实现，不会拦截权限消息。`node --check` 与 `go test ./src/internal/interaction/web` 通过。
  - 结论: 通过

- [x] Session JSON 不新增文件预览内容
  - 预期: 文件栏目录列表和文件正文不写入会话历史，不增加 session JSON 体积。
  - 实际: 目录列表和文件预览均通过 WebSocket 只读请求返回到前端内存；`read_project_file` 的正文只渲染在 `#project-file-modal`，没有追加到会话消息，也未修改 session 持久化逻辑。
  - 结论: 通过

- [x] 全项目测试通过
  - 预期: `go test ./...` 通过。
  - 实际: `node --check src/internal/interaction/web/static/app.js`、`go test ./src/internal/interaction/web`、`go test ./src/internal/interaction/web -run ProjectFile` 均通过；首次 `go test ./...` 在 `src/internal/tool/builtin` 因测试 helper 缺失编译失败，补充 `test_helpers_test.go` 后 `go test ./src/internal/tool/builtin` 与 `go test ./...` 均通过。
  - 结论: 通过
