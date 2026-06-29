## §11 错误排查

### 常见 5 类报错与修复

#### 1. JSON 语法错

**报错信息**:`解析配置文件失败(请检查 JSON 格式): <yaml 错误>`

**常见原因**:
- 末尾漏逗号 / 多余逗号(最后一个字段后写了 `,`);
- 引号未闭合(`"api_key": "sk-ant-xxx` 漏右引号);
- 字符串里包含未转义的双引号(应写 `\"`)。

**修复**:用 IDE / `python -m json.tool` / `jq .` 校验语法。

#### 2. 字段名拼写错

**报错信息**:`配置校验失败: XXX 不能为空` 或「字段不生效但启动未报错」

**常见原因**:
- `api_key` 写成 `apiKey` / `apikey`;
- `context_window_size` 写成 `contextWindowSize` / `context-window-size`;
- `tool_execution_timeout_seconds` 拼写错;
- MCP server 的 `handshake_timeout_seconds` 写成 `handshakeTimeout`。

**修复**:对照本 Skill 各 section 的 `JSON schema 摘要` 核对字段名(JSON 字段名用 snake_case,不是 camelCase)。

#### 3. 字段值类型错

**报错信息**:启动未报但行为异常 / JSON parse 阶段隐式失败

**常见原因**:
- 数字写成了字符串:`"max_tokens": "16384"` ❌ → `"max_tokens": 16384` ✓
- 布尔写成了字符串:`"enabled": "true"` ❌ → `"enabled": true` ✓
- 数组写成了字符串:`"enabled": "ReadFile,Bash"` ❌ → `"enabled": ["ReadFile", "Bash"]` ✓

**修复**:数字不加引号、布尔不加引号、数组用 `[]` 包裹。

#### 4. 未知字段警告

**行为**:CodePilot 当前版本对未知字段**静默忽略**(不报错也不警告)。

**风险**:把 `permisions` 写成 `permissions`(少了 s) → 启动成功但权限规则不生效,
只能靠行为异常反推。

**修复**:改写完成后用 `jq . ~/.codepilot/setting.json` 重新查看文件结构,确认关键 section
(`provider` / `api_key` / `mcp` / `permissions` / `compaction` 等)都在顶层或子层正确位置。

#### 5. 启动失败的常见 5 类报错

| 报错 | 原因 | 修复 |
|------|------|------|
| `配置文件不存在: <path>` | 路径无文件 | 复制 `config/setting.example.json` 到目标路径 |
| `解析配置文件失败(请检查 JSON 格式)` | JSON 语法错 | 用 IDE / `jq` 校验修复 |
| `配置校验失败: provider 不能为空` | 顶层 `provider` 缺失 | 补 `"provider": "anthropic"` |
| `配置校验失败: 不支持的供应商 "X"` | provider 不是合法值 | 改为 `anthropic` / `openai` |
| `配置校验失败: api_key 不能为空` | `api_key` 缺失 | 补 `api_key` 字段 |
| `配置校验失败: mcp.servers[N] (name=X) type=stdio 必须填写 command` | stdio 缺 command | 补 `command` 字段 |
| `配置校验失败: mcp.servers[N] (name=X) 缺少 type(必须是 stdio/http)` | type 缺失 | 加 `"type": "stdio"` / `"http"` |

### 排查工作流

```
启动报错
  ├─ 含 "配置文件不存在"        → 创建文件,复制 setting.example.json
  ├─ 含 "解析配置文件失败"       → JSON 语法错,用 jq / IDE 校验
  ├─ 含 "配置校验失败: <字段>"  → 对照 §11 第 5 类报错表修复
  ├─ 含 "mcp.servers[N]"        → 跳到 §2 mcp 错误排查
  └─ 无报错但行为异常          → 跳到 §11 第 4 类(未知字段)检查字段名拼写
```

### 工具快捷

```bash
# 校验 JSON 语法
jq . ~/.codepilot/setting.json

# 定位字段(看实际生效的配置)
jq '.mcp.servers | length' ~/.codepilot/setting.json
jq '.permissions.rules' ~/.codepilot/setting.json

# 备份再改写
cp ~/.codepilot/setting.json ~/.codepilot/setting.json.bak
```
