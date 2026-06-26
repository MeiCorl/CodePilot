// Package sources（codebase_awareness.go）实现「代码自感知」Source（Step 10.2）。
//
// 在 System Prompt 注入一段极简自描述（约 40-50 token），告诉 Agent：
//  1. CodePilot 自身的架构 / 模块设计 / 实现原理 / 关键流程可以查
//     `codebase-overview` Skill（精确匹配 frontmatter name）
//  2. 该 Skill 是「总索引 + 按需子文档」二级加载：SKILL.md 是模块目录索引，
//     详细实现原理在各 module md 中（reference/*.md）
//  3. 加载流程：先 use_skill("codebase-overview") 拿索引 → 再用 ReadFile
//     按需读取具体模块的子 md
//
// 设计原则（与 spec.md 对齐）：
//   - 零成本降级：本 Source 是无状态 struct，不读文件、不读 env，纯静态输出，
//     失败兜底也是「输出固定文案」，无需 try/catch 与降级分支
//   - 不污染常驻 SP：~40-50 token，详细实现原理全部进 Skill 按需加载
//   - skill.enabled=false 降级：本段仍生效（自描述与 Skill 可用性解耦，
//     即使 Skill 不可用 Agent 至少知道「CodePilot 自身原理在哪、Skill 不可用」）
//   - Anthropic Prompt Caching：Placement=System + Cacheable=true（Source 默认），
//     本段作为稳定缓存段减少重复 token 计费
//
// [架构分层] 本 Source 归 sources 包（引擎层，第 2 层），与既有 static/environment/
// memory_index / config_awareness 同包；不依赖 skill 包（Skill 加载在工具层，
// 第 3 层），避免上层引擎层反向依赖下层工具层造成循环依赖。
package sources

import (
	"context"

	"github.com/MeiCorl/CodePilot/src/internal/engine/prompt/tokens"
)

// codebaseAwarenessContent 是 CodebaseAwarenessSource 产出的固定自描述文本。
//
// 内容覆盖 4 个关键信息点（按 spec.md「能力清单 §1」对齐）：
//   - "codebase-overview" Skill 名称（精确匹配 frontmatter `name`，便于 LLM 调 use_skill）
//   - 一句话引导：CodePilot 自身的架构 / 模块设计 / 实现原理 / 关键流程都可以查该 Skill
//   - 提示该 Skill 是「总索引 + 按需子文档」二级加载
//   - 加载路径指引：use_skill 拿索引 → ReadFile 子 md
//
// 使用 Go 原始字符串（反引号）保留多行格式；无模板变量替换（纯静态）。
// 目标长度 < 50 token（spec.md 非功能要求），由 tokens.Estimate 实测保证。
//
// [Why XML 风格标签] 与 static.go / environment.go / memory_index.go /
// config_awareness.go 风格一致，标签让 LLM 明确感知到「这是规约边界」，
// 方便后续定位/截取。
const codebaseAwarenessContent = `<codebase_awareness>架构/实现:skill "codebase-overview";use_skill+ReadFile 子文档</codebase_awareness>`

// CodebaseAwarenessSource 实现 Source 接口，产出 ~40-50 token 的「代码自感知」段。
//
// 行为约定：
//  1. 无状态：可为零值 struct NewCodebaseAwarenessSource() 返回 &CodebaseAwarenessSource{}
//  2. 纯静态：Assemble 不读文件、不读 env 任何字段、不做 ctx 取消检查
//  3. 永远成功：固定返回 codebaseAwarenessContent，error 始终为 nil
//  4. Placement=System：进入 Anthropic system 字段，触发 prompt cache 复用
//
// 与既有 Source 的差异：
//   - 不像 StaticSource 那样由 5 段子模块拼接（单一职责，只描述一件事：CodePilot 自身原理入口）
//   - 不像 EnvironmentSource 那样读 OS/CWD/Git（无 IO、无 env 依赖）
//   - 不像 MemoryIndexSource 那样读 autolearn.Store（无外部依赖）
//   - 与 ConfigAwarenessSource 范式一致（同为「自描述」类 Source，
//     静态常量 + 零值 struct + 纯静态 Assemble）
//
// 参数 ctx/env 保留仅为满足 Source 接口签名；调用方传什么不影响产出。
type CodebaseAwarenessSource struct{}

// NewCodebaseAwarenessSource 构造一个代码自感知 Source 实例。
//
// 无状态；调用方按 Builder 链尾顺序追加即可（与其他 Source 解耦、零依赖）。
func NewCodebaseAwarenessSource() *CodebaseAwarenessSource { return &CodebaseAwarenessSource{} }

// Name 实现 Source 接口。固定返回 "codebase_awareness"，与 Builder Stats /
// WebUI SP 可观测性面板的展示 key 一致。
func (s *CodebaseAwarenessSource) Name() string { return "codebase_awareness" }

// Assemble 产出固定的 codebase 自描述 Section，Placement=System。
//
// 行为细节：
//   - 不使用 ctx：不取消、不超时（纯静态输出零成本）
//   - 不使用 env：无任何字段读取
//   - 不返回 error：固定文案不会失败（即便 IO/env 全坏也能正常注入）
//   - Tokens 由 tokens.Estimate 实时估算（rune 数 / 2，向上取整）
//
// 该方法被 Builder 在每次会话/切换会话时调用；纯静态确保多次调用结果完全一致
// （满足 Source 接口的「纯函数 + 可并发」契约）。
func (s *CodebaseAwarenessSource) Assemble(_ context.Context, _ Env) (Section, error) {
	return Section{
		Name:      "codebase_awareness",
		Content:   codebaseAwarenessContent,
		Placement: PlacementSystem,
		Tokens:    tokens.Estimate(codebaseAwarenessContent),
	}, nil
}
