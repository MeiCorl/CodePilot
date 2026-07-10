# Step 1.6 - WebUI 项目侧栏多标签 - 验收清单

- [x] 项目侧栏显示文件、Git、搜索三个 tab，且默认仍展示文件列表
  - 预期: 启动 WebUI 后右侧侧栏可切换三个 tab；文件 tab 首屏加载项目根目录。
  - 实际: Task 4 静态检查通过，`index.html` 已新增文件/Git/搜索三 tab DOM，文件 tab 保持 `is-active` 且 Git/搜索 tabpanel 默认 `hidden`。
  - 结论: 通过（Task 4 DOM/CSS 范围）

- [x] 文件 tab 保持 Step 1.5 原有能力
  - 预期: 可以进入子目录、返回上级、使用面包屑跳转、刷新目录、打开可预览文件。
  - 实际: Task 4 静态检查通过，原 `project-refresh-btn`、`project-current-path`、`project-breadcrumbs`、`project-panel-*` 状态容器和 `project-file-list` DOM 均保留在文件 tab 内。
  - 结论: 通过（Task 4 DOM/CSS 范围）

- [x] Git tab 展示当前项目有变更的文件列表
  - 预期: 在 Git 仓库中修改、新增、删除或创建未跟踪文件后，Git tab 刷新能看到对应条目和状态。
  - 实际: Task 1 后端 `NewProjectGitBrowser(root).Status()` 已通过单测覆盖修改、新增、删除、重命名、未跟踪文件；前端 Git tab 接入待后续 Task 验证。
  - 结论: Task 1 后端通过；端到端展示待后续 Task 验证

- [ ] 点击 Git 变更文件后展示左右栏变更内容
  - 预期: 变更前内容在左侧，变更后内容在右侧；新增、删除、未跟踪文件能以空侧或提示处理。
  - 实际: Task 1 后端 `ReadDiff` 已通过单测覆盖修改、新增、删除、重命名、未跟踪文件的 before/after 内容；前端点击展示待后续 Task 验证。
  - 结论: Task 1 后端通过；前端展示待后续 Task 验证

- [ ] Git 异常场景稳定降级
  - 预期: 非 Git 仓库、Git 命令不可用、二进制文件、大文件或读取失败时，前端展示可理解错误或不可预览提示，不崩溃。
  - 实际: Task 1 后端已通过单测覆盖非 Git 仓库、Git 不可用、越界路径、未变更文件、二进制文件和大文件的稳定 reason。
  - 结论: Task 1 后端通过

- [x] 搜索 tab 支持普通关键词搜索
  - 预期: 输入关键词并执行搜索后，返回包含关键词的文件列表、行号与命中片段。
  - 实际: Task 2 后端 ProjectSearcher 单测覆盖普通关键词搜索，返回文件路径、行号、命中片段、语言和渲染类型。
  - 结论: 通过
- [x] 搜索 tab 支持指定路径
  - 预期: 填写相对路径后，搜索范围限制在该目录或文件内，不返回范围外结果。
  - 实际: Task 2 后端 ProjectSearcher 单测覆盖 Path 限定，只返回指定目录内命中；越界路径返回 outside_workdir。
  - 结论: 通过
- [x] 搜索 tab 支持正则匹配
  - 预期: 启用正则后按正则表达式匹配内容；非法正则展示错误原因。
  - 实际: Task 2 后端 ProjectSearcher 单测覆盖正则命中列号与非法正则 invalid_regex reason。
  - 结论: 通过
- [x] 搜索 tab 支持排除特定文件或目录
  - 预期: 设置排除规则后，被排除文件或目录中的命中不会出现在结果中。
  - 实际: Task 2 后端 ProjectSearcher 单测覆盖 logs/** 排除规则，并默认跳过 .git 隐藏目录。
  - 结论: 通过
- [x] 搜索边界和性能保护生效
  - 预期: 越界路径被拒绝；二进制、大文件和常见构建产物被跳过；结果过多时标记截断。
  - 实际: Task 2 后端 ProjectSearcher 单测覆盖越界/逃逸 symlink 拒绝、二进制和大文件跳过、扫描文件数/总命中数/单文件命中数/片段长度限制。
  - 结论: 通过
- [ ] 快速切换 tab、重复刷新或重复搜索不会显示旧回包
  - 预期: 前端只渲染最后一次请求对应结果，旧响应不会覆盖当前视图。
  - 实际: 待验证
  - 结论: 待验证

- [x] 窄屏下项目侧栏多标签布局可用
  - 预期: tab、Git 列表、搜索表单和结果列表不遮挡主对话区和输入框，长路径不会撑破容器。
  - 实际: Task 4 静态检查通过，CSS 在 860px/620px 下保持项目栏为独立 grid 区域，Git/搜索列表内部滚动，搜索表单限制高度，路径与片段使用 `overflow-wrap`/`word-break` 约束。
  - 结论: 通过（Task 4 DOM/CSS 范围）

- [ ] 现有交互不回归
  - 预期: 会话列表、消息发送、流式输出、工具 diff 弹窗、权限确认、/skills、/sessions 均可正常使用。
  - 实际: 待验证
  - 结论: 待验证

- [x] 后端测试通过
  - 预期: Web 包相关单测通过，全项目 `go test ./...` 通过或失败项与本步骤无关并有说明。
  - 实际: 已运行 `go test ./src/internal/interaction/web -run ProjectGit`，测试通过；已运行 `go test ./src/internal/interaction/web -run ProjectSearcher`，测试通过。
  - 结论: Task 1、Task 2 范围通过
- [ ] 端到端验收通过
  - 预期: 启动 WebUI 后，用户可在一个右侧项目侧栏内完成文件浏览、Git 变更查看、内容搜索三条链路。
  - 实际: 待验证
  - 结论: 待验证
## 2026-07-10 验证更新

- [x] 前端静态语法检查：`node --check src/internal/interaction/web/static/app.js` 通过。
- [x] Step 1.6 相关后端链路：`go test ./src/internal/interaction/web -run "Project(Git|Search).*Handler|ProjectGit|ProjectSearcher" -count=1 -v` 通过；Windows 非管理员环境下 symlink escape case 按测试逻辑 skip。
- [x] Git/Search 前端请求均使用 `request_id` / 序列号过滤旧回包；断线时会清理 Git diff、Git list、Search pending 状态。
- [ ] 全项目验证：`go test ./...` 已执行但未完全通过；失败项为 `src/internal/interaction/web` 既有 `TestBusyRejectsConcurrentInput`，单独复跑仍因读取 busy 响应超时失败，本次新增 Git/Search 定向测试通过。
- [ ] 浏览器人工 E2E：未在当前回合启动完整 WebUI（主程序会自动调起系统浏览器）；需后续人工确认文件/Git/Search 三个 tab 的实际页面交互。