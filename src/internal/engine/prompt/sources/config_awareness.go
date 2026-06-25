// Package sources（config_awareness.go）实现「配置自感知」Source（Step 10.1）。
//
// 在 System Prompt 注入一段极简自描述（约 60-80 token），告诉 Agent：
//  1. 配置文件的两层路径：全局 ~/.codepilot/setting.json 与项目级 <cwd>/.codepilot/setting.json
//     （项目级覆盖全局，合并语义）
//  2. 详细 schema/示例/默认值请通过 Skill 系统加载 config-management
//  3. 改写 setting.json 使用 ReadFile + EditFile/WriteFile 工具
//
// 设计原则（与 spec.md 对齐）：
//   - 零成本降级：本 Source 是无状态 struct，不读文件、不读 env，纯静态输出，
//     失败兜底也是「输出固定文案」，无需 try/catch 与降级分支
//   - 不污染常驻 SP：~60-80 token，详细 schema 全部进 Skill 按需加载
//   - skill.enabled=false 降级：本段仍生效（自描述与 Skill 可用性解耦，
//     即使 Skill 不可用 Agent 至少知道「配置文件在哪、Skill 不可用」）
//   - Anthropic Prompt Caching：Placement=System + Cacheable=true（Source 默认），
//     本段作为稳定缓存段减少重复 token 计费
//
// [架构分层] 本 Source 归 sources 包（引擎层，第 2 层），与既有 static/environment/
// memory_index 同包；不依赖 skill 包（Skill 加载在工具层，第 3 层），
// 避免上层引擎层反向依赖下层工具层造成循环依赖。
package sources

import (
	"context"

	"github.com/MeiCorl/CodePilot/src/internal/engine/prompt/tokens"
)

// configAwarenessContent 是 ConfigAwarenessSource 产出的固定自描述文本。
//
// 内容覆盖 4 个关键信息点（按 spec.md 「能力清单 §1」对齐）：
//   - 两层配置文件路径 + 合并/覆盖语义
//   - "config-management" Skill 名称（精确匹配 frontmatter `name`，便于 LLM 调 use_skill）
//   - 一句话引导：详细 schema/示例/默认值见该 Skill
//   - 改写工具指引：ReadFile + EditFile/WriteFile（Step 2 内置）
//
// 使用 Go 原始字符串（反引号）保留多行格式；无模板变量替换（纯静态）。
// 目标长度 < 80 token（spec.md 非功能要求），由 tokens.Estimate 实测保证。
//
// [Why XML 风格标签] 与 static.go / environment.go / memory_index.go 风格一致，
// 标签让 LLM 明确感知到「这是规约边界」，方便后续定位/截取。
const configAwarenessContent = `<config_awareness>~/.codepilot/setting.json + <cwd>/.codepilot/setting.json. ReadFile+EditFile/WriteFile; see skill "config-management".</config_awareness>`

// ConfigAwarenessSource 实现 Source 接口，产出 ~60-80 token 的「配置自感知」段。
//
// 行为约定：
//  1. 无状态：可为零值 struct NewConfigAwarenessSource() 返回 &ConfigAwarenessSource{}
//  2. 纯静态：Assemble 不读文件、不读 env 任何字段、不做 ctx 取消检查
//  3. 永远成功：固定返回 configAwarenessContent，error 始终为 nil
//  4. Placement=System：进入 Anthropic system 字段，触发 prompt cache 复用
//
// 与既有 Source 的差异：
//   - 不像 StaticSource 那样由 5 段子模块拼接（单一职责，只描述一件事：配置位置）
//   - 不像 EnvironmentSource 那样读 OS/CWD/Git（无 IO、无 env 依赖）
//   - 不像 MemoryIndexSource 那样读 autolearn.Store（无外部依赖）
//
// 参数 ctx/env 保留仅为满足 Source 接口签名；调用方传什么不影响产出。
type ConfigAwarenessSource struct{}

// NewConfigAwarenessSource 构造一个配置自感知 Source 实例。
//
// 无状态；调用方按 Builder 链尾顺序追加即可（与其他 Source 解耦、零依赖）。
func NewConfigAwarenessSource() *ConfigAwarenessSource { return &ConfigAwarenessSource{} }

// Name 实现 Source 接口。固定返回 "config_awareness"，与 Builder Stats /
// WebUI SP 可观测性面板的展示 key 一致。
func (s *ConfigAwarenessSource) Name() string { return "config_awareness" }

// Assemble 产出固定的 config 自描述 Section，Placement=System。
//
// 行为细节：
//   - 不使用 ctx：不取消、不超时（纯静态输出零成本）
//   - 不使用 env：无任何字段读取
//   - 不返回 error：固定文案不会失败（即便 IO/env 全坏也能正常注入）
//   - Tokens 由 tokens.Estimate 实时估算（rune 数 / 2，向上取整）
//
// 该方法被 Builder 在每次会话/切换会话时调用；纯静态确保多次调用结果完全一致
// （满足 Source 接口的「纯函数 + 可并发」契约）。
func (s *ConfigAwarenessSource) Assemble(_ context.Context, _ Env) (Section, error) {
	return Section{
		Name:      "config_awareness",
		Content:   configAwarenessContent,
		Placement: PlacementSystem,
		Tokens:    tokens.Estimate(configAwarenessContent),
	}, nil
}