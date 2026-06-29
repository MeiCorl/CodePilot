## §8 hook — Hook 钩子系统配置

### 路径说明

`hook` 段控制 Step 11 Hook 系统。Hook 让 CodePilot 在生命周期事件上执行用户配置的动作,例如工具执行前后记录日志、HTTP 通知、提示词注入或轻量 LLM 检查。支持 command/http/prompt/agent 四种 action。配置可放全局或项目级;项目级 `entries` 非空时整体覆盖全局 `entries`。

### JSON schema 摘要

```jsonc
{
  "hook": {
    "enabled": true,
    "entries": [
      {
        "name": "format-go-after-write",
        "event": "post_tool_use",
        "condition": {
          "all": [
            { "field": "tool_name", "op": "eq", "value": "WriteFile" },
            { "field": "tool_input.file_path", "op": "glob", "value": "*.go" }
          ]
        },
        "action": {
          "type": "command",
          "command": "gofmt -w $TOOL_INPUT_FILE_PATH",
          "timeout": "10s"
        },
        "async": false,
        "once": false
      }
    ]
  }
}
```

### 字段说明

| 字段 | 默认 | 说明 |
|------|------|------|
| `hook.enabled` | true | `false` 时 Hook 引擎跳过所有 entries |
| `hook.entries[]` | [] | Hook 规则数组;项目级非空数组整体覆盖全局数组 |
| `name` | 必填 | Hook 名称,用于日志与 once 追踪 |
| `event` | 必填 | 12 类事件之一,见下表 |
| `condition` | 无 | 省略 / null / 空对象表示总是匹配 |
| `action` | 必填 | 四选一:`command` / `http` / `prompt` / `agent` |
| `async` | false | true 时后台执行,不阻塞 Agent 主流程 |
| `once` | false | true 时同一会话内同名 Hook 只触发一次 |

### 事件列表

| event | 触发时机 |
|------|----------|
| `program_start` | 进程启动、配置与集成装配完成后 |
| `program_exit` | 进程退出前 |
| `compact` | 上下文压缩完成后 |
| `error` | Agent Loop 不可恢复错误前 |
| `session_start` | 新建或恢复会话成功后 |
| `session_end` | 清空或切换当前会话前 |
| `iteration_start` | Agent Loop 每轮 LLM 调用前 |
| `iteration_end` | Agent Loop 每轮结束后 |
| `pre_tool_use` | 工具执行前、权限检查前 |
| `post_tool_use` | 工具执行后,无论成功或失败 |
| `pre_message` | 用户消息发送给 LLM 前 |
| `post_message` | LLM 回复写入 history 后 |

### condition DSL

Leaf 条件形如:

```json
{ "field": "tool_name", "op": "eq", "value": "WriteFile" }
```

- `field`:支持 `event`、`tool_name`、`tool_input_file_path`、`message_role`、`session_id`、`workdir`、`error`、`iteration`、`tool_duration_ms`、`tool_is_error`,以及 `tool_input.<key>`。
- `op`:支持 `eq`(默认)、`neq`、`glob`、`contains`。
- 组合条件:`all: []` 表示全部子条件匹配才触发,空数组视为 true;`any: []` 表示任一子条件匹配即触发,空数组视为 false。

### action 类型

#### command

```json
{ "type": "command", "command": "gofmt -w $TOOL_INPUT_FILE_PATH", "working_dir": "", "env": {"NO_COLOR":"1"}, "timeout": "10s" }
```

本地命令。`working_dir` 为空时取当前工作目录;`timeout` 默认 30s;非 0 退出码只记 warn,不中断 Agent 主流程。

#### http

```json
{ "type": "http", "method": "POST", "url": "https://example.com/hook", "headers": {"Content-Type":"application/json"}, "body": "{\"file\":\"$TOOL_INPUT_FILE_PATH\"}", "timeout": "5s" }
```

发送 HTTP 请求。`url` 必须是 http/https;2xx 视为成功;响应不会写入对话。

#### prompt

```json
{ "type": "prompt", "text": "Go 文件请使用 gofmt: $TOOL_INPUT_FILE_PATH", "as": "system_reminder" }
```

把文本注入当前或下一轮 user 消息尾部。`as` 当前只支持 `system_reminder`,运行时会包装为 `<system-reminder>...</system-reminder>`;不写入历史消息。

#### agent

```json
{ "type": "agent", "prompt": "检查刚写入的文件是否有明显风险: $TOOL_INPUT_FILE_PATH", "allow_tools": [], "max_iterations": 1, "timeout": "60s" }
```

轻量 LLM 子任务 stub。当前固定最多 1 轮,不写回主会话 history;Step 12 SubAgent 完成后会升级为完整子代理。

### 可插值变量

| 变量 | 含义 |
|------|------|
| `$EVENT` | 当前事件名 |
| `$SESSION_ID` | 当前会话 ID |
| `$ITERATION` | 当前 Agent 轮次 |
| `$WORKDIR` | 当前工作目录 |
| `$TOOL_NAME` | 工具名 |
| `$TOOL_INPUT_FILE_PATH` | 工具参数里的 `file_path` 或 `path` |
| `$TOOL_RESULT` | post_tool_use 的工具结果 |
| `$TOOL_IS_ERROR` | 工具是否失败 |
| `$TOOL_DURATION_MS` | 工具耗时毫秒 |
| `$MESSAGE_ROLE` | `user` 或 `assistant` |
| `$MESSAGE_CONTENT` | 消息内容 |
| `$ERROR` | 错误文本 |
| `$TOOL_INPUT.command` | `tool_input` map 的子字段示例 |

未定义变量会替换为空字符串,`$$FOO` 输出字面量 `$FOO`。

### 完整示例

```json
{
  "hook": {
    "enabled": true,
    "entries": [
      {
        "name": "log-start",
        "event": "program_start",
        "action": { "type": "command", "command": "echo CodePilot started in $WORKDIR" }
      },
      {
        "name": "notify-write",
        "event": "post_tool_use",
        "condition": { "field": "tool_name", "value": "WriteFile" },
        "action": {
          "type": "http",
          "method": "POST",
          "url": "https://example.com/codepilot-hook",
          "headers": { "Content-Type": "application/json" },
          "body": "{\"file\":\"$TOOL_INPUT_FILE_PATH\",\"failed\":\"$TOOL_IS_ERROR\"}"
        },
        "async": true
      },
      {
        "name": "remind-go-style",
        "event": "pre_tool_use",
        "condition": {
          "all": [
            { "field": "tool_name", "op": "eq", "value": "WriteFile" },
            { "field": "tool_input.file_path", "op": "glob", "value": "*.go" }
          ]
        },
        "action": { "type": "prompt", "text": "写 Go 文件后请保持 gofmt 格式。", "as": "system_reminder" }
      }
    ]
  }
}
```

### 是否需要重启

**需要重启**。Hook 配置在启动期读取并装配,运行期不热加载。

### 错误排查

- Hook 不触发 → 确认 `hook.enabled` 不是 `false`,事件名合法,修改后已重启。
- 条件不匹配 → 临时删除 `condition` 验证事件是否触发,再逐项恢复 leaf/all/any。
- 路径 glob 异常 → 优先使用 `tool_input.file_path`;pattern 中建议用 `/`,Windows 反斜杠会兼容。
- prompt 没生效 → 确认 `type=prompt` 且 `as=system_reminder`;注入文本影响发送给 LLM 的消息视图,不一定出现在历史记录里。
- 查看运行情况 → WebUI SP 面板有 `hooks_awareness`;状态栏 hooks 子项显示已配置数、触发数、失败数;详细错误看 CodePilot 日志。

---
