package adapter

import (
	"context"

	"github.com/gorilla/websocket"

	"github.com/MeiCorl/CodePilot/src/internal/command/slash"
)

// SkillsListCmd 实现 /skills client 类 slash 命令（Step 10 Task 6）。
//
// 触发链路：
//  1. 用户在输入框输入 / → 候选下拉显示 /skills（带紫色 "skill" 标签）
//  2. 用户选中 /skills → 前端识别 category==="client" && name==="/skills"
//     → 调 openSkillsTable() 走本地逻辑，不调 Execute
//  3. 前端向 WS 发 list_skills → 后端 handleSkills → 遍历 SkillProvider
//     ListBySource() → 回推 skills_list payload → 前端渲染模态框
//
// [Why Execute 返回 nil] spec §B.4 明确规定 Category="client" 类命令不通过
// Execute 发起 WS 调用，由前端识别后走本地逻辑（同 /sessions 一致）。Execute
// 是占位实现，确保满足 slash.SlashCommand 接口即可，不执行任何后端业务。
//
// 路径：该文件位于 src/internal/skill/adapter/ 而非 command/slash/builtin.go，
// 是因为 /skills 是 Skill 系统的一部分（消费方是 Skill 管理），与 Skill
// 命令实现放在同一子包便于维护；注册入口在 main.go 顶层通过
// slashRegistry.Register(&skilladapter.SkillsListCmd{}) 完成。
type SkillsListCmd struct{}

// Name 返回命令名（含前导 "/"）。
//
// 固定返回 "/skills"：与 spec §B.4 一致，与前端 app.js 的 openSkillsTable
// 识别硬编码保持一致。Slash Registry 按 Name 唯一索引，重复注册会失败。
func (c *SkillsListCmd) Name() string { return nameSkills }

// Description 返回命令描述，会在候选下拉中展示给用户。
func (c *SkillsListCmd) Description() string { return descSkills }

// NeedsArg 表示命令是否需要用户补充参数。/skills 无需参数。
func (c *SkillsListCmd) NeedsArg() bool { return false }

// ArgHint 参数占位提示；NeedsArg=false 时返回空字符串。
func (c *SkillsListCmd) ArgHint() string { return "" }

// Category 返回命令分类标识。"client" 类命令由前端识别后走本地逻辑
// （调 openSkillsTable → 弹 /skills 模态框），不通过 Execute 发起 WS 调用。
//
// [Why] 与 builtin.go 的 sessionsCmd 风格保持一致（CategoryClient="client"）。
// 通过 Category 字段把"前端拦截"语义传给前端 app.js 的 applySlashCompletion。
func (c *SkillsListCmd) Category() string { return slash.CategoryClient }

// Execute 占位实现：始终返回 nil，由前端识别 Category 后走本地逻辑。
//
// 入参 _ / _ / _ 显式忽略：ctx 已由 category 拦截机制走不到这里；
// conn 在 client 类场景下不使用；arg 为空字符串。
func (c *SkillsListCmd) Execute(ctx context.Context, conn *websocket.Conn, arg string) error {
	_ = ctx
	_ = conn
	_ = arg
	return nil
}

// ---- 命令元数据常量 ----
//
// 命令名 / 描述集中定义，避免散落字面量（与 builtin.go 的 nameNew/descNew 等风格一致）。
const (
	// nameSkills 是 /skills 命令名（含前导 "/"）。
	nameSkills = "/skills"
	// descSkills 是 /skills 命令描述，会在候选下拉中展示给用户。
	descSkills = "列出当前系统支持的所有 Skill（区分项目级/用户级/内置级）"
)
