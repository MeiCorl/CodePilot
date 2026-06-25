package adapter

import (
	"context"
	"fmt"
	"strings"

	"github.com/gorilla/websocket"

	"github.com/MeiCorl/CodePilot/src/internal/command/slash"
	"github.com/MeiCorl/CodePilot/src/internal/skill"
)

// ---- 分类常量 ----

// CategorySkill 是 Skill slash 命令的 Category 值,前端在候选下拉中据此分组渲染
// 并在工具块头部附加紫色「skill」徽标(spec §E.2)。
//
// [Why] 与 command/slash/builtin.go 定义的 CategorySession/Context/Debug/Client
// 风格保持一致:全小写语义字符串;Skill 命令新增一个独立类别,便于前端
// 「/skills」模态框以外仍能以紫色标签识别 Skill 类命令。
const CategorySkill = "skill"

// ---- 接口定义 ----

// LeadMessageInjector 是 slash 适配器对上层 handler 的最小接口。
//
// 适配器在 Execute 时需要把 Skill 完整内容 + 可选 <user_args> 段追加到
// 对话首条 user 消息(LeadUserMessage),由 web.Handler 在收到命令时转发给
// *conversation.ConversationManager.SetLeadUserMessage。
//
// [Why] 把接口定义放在 adapter 包内(spec §B.3 + tasks.md Task 4 硬性约束):
//   - 避免 adapter → engine/conversation / web 的反向依赖,守住 5 层架构边界;
//   - 适配器只关心「注入一段文本到 LeadUserMessage」的语义,具体的存储/转换
//     由 main.go 顶层装配时把 *web.Handler / *ConversationManager 适配为
//     LeadMessageInjector。
//
// InjectLeadUserMessage 参数约定:
//
//   - content:Skill 完整 markdown 内容(由 skill.Body() 拼出,含重组后的
//     # Skill: <name> 标题段 + description + 可选 args 提示 + 正文);
//   - userArg:用户在 /<skill> 命令后追加的参数文本;空字符串表示无参数,
//     此时 content 末尾不追加 <user_args> 段。
//
// 返回值:
//
//   - nil:注入成功;
//   - 非 nil error:由 web.Handler 捕获后回推 stream_error 给前端。
type LeadMessageInjector interface {
	InjectLeadUserMessage(content, userArg string) error
}

// ---- Skill → SlashCommand 适配器实现 ----

// skillCmd 是单个 Skill 适配成的 SlashCommand。
//
// 字段:
//
//	- skill:被适配的 Skill 指针,Execute 时通过 Body() 拿到重组后的完整内容;
//	- h:LeadMessageInjector 引用,Execute 把内容+arg 注入到对话 LeadUserMessage。
//
// 生命周期:
//
//   - 由 AsSlashCommand 在启动期构造,一次性注册到 slash.Registry;
//   - 后续 Execute 调用走 registry.Get(name) 拿到同一实例复用,无需每次新建;
//   - 适配器本身无内部状态,所有数据来自 *skill.Skill 字段。
type skillCmd struct {
	skill *skill.Skill
	h     LeadMessageInjector
}

// 编译期断言:skillCmd 必须实现 slash.SlashCommand 接口,否则编译失败。
//
// [Why] 显式断言而非依赖 NewUseSkillTool / AsSlashCommand 的返回值类型:
//   - AsSlashCommand 返回 slash.SlashCommand 接口,一旦接口方法集与
//     skillCmd 的方法集不匹配,Register 时会 runtime 失败;
//   - 显式 var _ 断言把失败前移到编译期,CI 流水线第一时间捕获接口漂移。
var _ slash.SlashCommand = (*skillCmd)(nil)

// Name 返回 slash 命令名(含 / 前缀),全局唯一。
//
// 实现 slash.SlashCommand 接口。
//
// 约定:Skill 名称必须符合 [a-zA-Z0-9_-]+ 等 URL 安全的字符集(由 loader/registry
// 校验),前缀 "/" 由本方法补齐;若 Skill 名称为空则 AsSlashCommand 在调用 NewSkill
// 时已被 loader 拒绝,Register 时 cmd.Name() 也不应为空。
func (c *skillCmd) Name() string {
	return "/" + c.skill.Name
}

// Description 返回 Skill 的描述文本,前端候选下拉中展示给用户。
//
// 实现 slash.SlashCommand 接口。
func (c *skillCmd) Description() string {
	return c.skill.Description
}

// NeedsArg 表示命令是否需要用户补充参数。
//
// 规则:
//   - Skill 命令统一需要用户先补全参数/任务文本再提交;
//   - 前端选中后只把命令名补全到输入框,不直接触发执行。
//
// 实现 slash.SlashCommand 接口。
func (c *skillCmd) NeedsArg() bool {
	return true
}

// ArgHint 返回参数占位提示文本,仅在 NeedsArg()=true 时展示给用户。
//
// 实现 slash.SlashCommand 接口。
//
// [Why] 直接返回 Skill.Args:Skill 作者在 SKILL.md 中写的 args 提示(如
// "<path>" / "<module-name>")既适合 LLM 识别,又适合前端展示给用户作为
// 占位文本,无需 adapter 再做格式转换。
func (c *skillCmd) ArgHint() string {
	return c.skill.Args
}

// Category 返回命令分类标识,固定为 "skill"。
//
// 实现 slash.SlashCommand 接口(spec §B.2 + §E.2)。
//
// 前端识别 category==="skill" 时:
//   - 候选下拉条目左侧加紫色「skill」标签;
//   - 工具块头部加紫色「skill: <name>」徽标(Task 6 完成)。
func (c *skillCmd) Category() string {
	return CategorySkill
}

// Execute 把 Skill 完整内容 + 可选 <user_args> 段注入到对话首条 user 消息
// (LeadUserMessage),由 *conversation.ConversationManager 拼到下一轮 user 消息
// 头部,LLM 端感知到 Skill 指令后据此执行。
//
// 行为(spec §B.3):
//   - ctx 已取消 → 立即返回 ctx.Err(),不调 h.InjectLeadUserMessage;
//   - h 为 nil → 返回 "nil injector" 错误(防御性:AsSlashCommand 不应传入 nil);
//   - 正常路径:取 skill.Body() 拿到完整内容,若 arg 非空则末尾追加
//     "\n\n<user_args>\n<arg>\n</user_args>" 段,调 h.InjectLeadUserMessage
//     注入;若 injector 返回 error 则透传。
//
// 参数:
//
//   - ctx:支持通过 cancel 终止(用户中止 / WS 关闭);
//   - conn:触发该命令的 WebSocket 连接,当前实现不使用(保留参数以与接口约定对齐);
//   - arg:用户在 /<skill> 命令后追加的参数文本;无参时为空字符串。
//
// 返回值:
//
//   - nil:注入成功;
//   - ctx.Err():ctx 已取消;
//   - h.InjectLeadUserMessage 返回的 error:由 web.Handler 包装回推前端。
//
// [Why] 拼接 <user_args> 段:Skill 正文可能引用「用户提供的参数」,直接在
// content 末尾追加一段,LLM 端可以稳定识别(类似 <project_instructions> /
// <memory_index> 等 XML 包裹的注入段),与 spec §B.3「末尾追加 <user_args>」
// 的约定一致。
//
// [Why] 用 skill.Body() 而非 FullContent():slash 路径是用户手动触发,
// Body() 零 I/O 的缓存版即可,避免每次 Execute 触发 SKILL.md 二次读盘;
// 「最新内容」语义留给 use_skill 工具路径覆盖(参考 tool.go Execute)。
func (c *skillCmd) Execute(ctx context.Context, conn *websocket.Conn, arg string) error {
	// 1. ctx 取消检查(spec §B.3 约定:Execute 必须响应 ctx.Done()).
	if err := ctx.Err(); err != nil {
		return err
	}
	// 2. 防御性:AsSlashCommand 应当拒绝 nil injector;但 Run-time 仍兜底防止 panic.
	if c.h == nil {
		return fmt.Errorf("slash adapter: LeadMessageInjector is nil for skill %q", c.skill.Name)
	}
	// 3. 构造完整 content:Body() 拿到 # Skill: <name> 标题段 + description + 可选 args + 正文.
	content := c.skill.Body()
	// 4. arg 非空时追加 <user_args> 段,供 Skill 内部指令引用.
	if arg != "" {
		content = appendUserArgs(content, arg)
	}
	// 5. 委托给 handler 注入(具体由 main.go 顶层装配时把 *web.Handler 包装为
	//    LeadMessageInjector,内部走 *ConversationManager.SetLeadUserMessage).
	return c.h.InjectLeadUserMessage(content, arg)
}

// appendUserArgs 在 content 末尾追加 <user_args> 段,供 Skill 内部指令引用
// 用户提供的参数文本(spec §B.3「带参」触发模式)。
//
// 格式约定:
//
//	<原有 content>
//
//	<user_args>
//	<arg>
//	</user_args>
//
// [Why] 三段独立行:与 LeadUserMessage 中其他 XML 包裹段(<project_instructions> /
// <memory_index> 等)风格一致,LLM 端 XML 段解析器可稳定识别边界;
// 段前 "\n\n" 确保与前一段之间有空行,markdown 渲染时不与上一段挤成一行。
func appendUserArgs(content, arg string) string {
	var sb strings.Builder
	sb.Grow(len(content) + len(arg) + 32)
	sb.WriteString(content)
	sb.WriteString("\n\n<user_args>\n")
	sb.WriteString(arg)
	sb.WriteString("\n</user_args>")
	return sb.String()
}

// ---- 工厂与批量注册 ----

// AsSlashCommand 把一个 *skill.Skill 适配为 slash.SlashCommand 接口。
//
// 入参约束:
//
//   - s 不为 nil;为 nil 时返回 nil(调用方 Register 会被 slash.Registry 拒绝
//     "注册命令不能为 nil");
//   - h 不为 nil;为 nil 时 Execute 仍能在 ctx 取消前返回 error,适配器不会
//     panic(防御性兜底)。
//
// 返回值:slash.SlashCommand 接口(实际类型为 *skillCmd)。
//
// [Why] 返回接口而非 *skillCmd:与 tool.NewUseSkillTool 风格一致,调用方一行
// `r.Register(adapter.AsSlashCommand(s, h))` 完成注册,无需关心具体类型。
func AsSlashCommand(s *skill.Skill, h LeadMessageInjector) slash.SlashCommand {
	if s == nil {
		return nil
	}
	return &skillCmd{skill: s, h: h}
}

// RegisterAll 把一组 Skill 批量适配并注册到 slash.Registry,收集部分失败时的 errors。
//
// 行为:
//   - 任一 Skill 的 Name()(即 "/" + skill.Name)在 Registry 中已存在(冲突)
//     时,收集 error 到返回切片,继续处理后续 Skill;
//   - 入参 r 为 nil 时,直接返回 [ErrNilRegistry] 错误切片;
//   - h 为 nil 时,所有 Skill 都会因 skillCmd.Execute 内部 nil 兜底而在实际
//     执行时报错,本函数只把适配结果注册到 Registry,不在 Register 阶段检查 h;
//
// 返回值:
//
//   - nil 切片:全部成功注册;
//   - error 切片:部分失败的 error 集合(顺序与 skills 入参顺序一致);
//     已成功注册的 Skill 会保留在 Registry 中(与 slash.RegisterBuiltin 风格
//     一致,失败时不做原子回滚),由 main.go 决定是否 panic。
//
// [Why] 收集 errors 而非首个 error:Skill 数量可能较多,启动期一次性把全部
// 冲突打印出来,便于用户一次性修复多个重名;与 spec §B.1「name 冲突时由
// §A.4 规则预先消解」配合——主流程在 Skill 加载时已保证不冲突,RegisterAll
// 阶段理论上不会触发冲突,但保留错误收集路径作为安全网。
func RegisterAll(r *slash.Registry, skills []*skill.Skill, h LeadMessageInjector) []error {
	if r == nil {
		return []error{fmt.Errorf("skill adapter RegisterAll: slash.Registry is nil")}
	}
	var errs []error
	for _, s := range skills {
		if s == nil {
			errs = append(errs, fmt.Errorf("skill adapter RegisterAll: nil skill entry"))
			continue
		}
		cmd := AsSlashCommand(s, h)
		if err := r.Register(cmd); err != nil {
			errs = append(errs, fmt.Errorf("register /%s: %w", s.Name, err))
		}
	}
	return errs
}
