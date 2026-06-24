// Package adapter 把 skill 数据层投影到 CodePilot 其他子系统:
//   - tool.go  →  tool.Tool 实现(use_skill 工具,供 LLM 主动调用);
//   - slash.go →  slash.SlashCommand 实现(/<skill-name> 命令);
//
// 本包是 skill 系统的「适配层」,处于第 3 层 工具层内部,负责 skill 包与
// tool / slash / prompt 等上层接口之间的形态转换:
//
//   - skill 主包(Skill / Registry / Loader / Scanner)只关心数据层,不 import 任何上层;
//   - 本包不 import web / engine / prompt 等上层包,只 import skill + tool + stdlib;
//   - 适配对象注册到上层 Registry 的时机在 main.go 顶层装配(Task 7)完成。
//
// [Why] 不与 skill 主包合并:主包定位为「数据层」(只关心 SKILL.md 格式与合并规则),
// 不引入对 tool.Tool 接口的依赖——这样主包可以在任何 Agent 框架中被复用;
// 适配层放在独立子包,主包保持零工具依赖的纯净形态。
package adapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/MeiCorl/CodePilot/src/internal/skill"
	"github.com/MeiCorl/CodePilot/src/internal/tool"
)

// UseSkillName 是 use_skill 工具的唯一标识(snake_case,与 tool.Tool 约定一致)。
//
// [Why] 固定常量:与 tool.Registry 中其他工具命名风格对齐;前端 WebUI 工具块头部
// 通过 Name() === "use_skill" 判定是否加紫色「skill: <name>」徽标(Task 6 完成)。
const UseSkillName = "use_skill"

// useSkillDescription 是 use_skill 工具面向 LLM 的说明文本。
//
// 内容必须明确:
//   - 用途:按需加载 Skill 完整内容(渐进式披露的第二步);
//   - 输入:skill_name 来自 /skills 列表 或 system prompt 的 Skill 索引段;
//   - 失败:Skill 不存在时返回 error,LLM 自主决策重试/换名(spec §D.2)。
//
// [Why] 中文描述:与 CodePilot 内置工具的 Description 风格保持一致(均为中文),
// 便于 LLM 在中文场景下识别用途;跨语言场景由 provider 层自动翻译。
const useSkillDescription = "按需加载 Skill 的完整内容到上下文中。" +
	"Input: skill_name(Skill 名称,来自 /skills 列表或 system prompt 中的 Skill 索引段)"

// useSkillInputSchema 是 use_skill 工具的 JSON Schema,符合 tool.Tool.InputSchema 约定。
//
// 字段:
//   - skill_name:必填 string,LLM 传入要加载的 Skill 名称;
//
// [Why] 以 []byte 常量形式持有:避免每次调用重新 marshal,降低 LLM 调用路径的
// 重复序列化开销;格式与 spec §D.1 完全对齐。
var useSkillInputSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "skill_name": {
      "type": "string",
      "description": "要加载的 Skill 名称"
    }
  },
  "required": ["skill_name"]
}`)

// useSkillInput 是 use_skill 工具的入参结构,Execute 内部 JSON 反序列化目标。
//
// [Why] 独立 struct 而非 map[string]string:编译期类型检查 + jsonschema tag 自动生成,
// 与现有 ReadFile / Grep 等内置工具的入参风格保持一致。
type useSkillInput struct {
	SkillName string `json:"skill_name"`
}

// useSkillTool 是 use_skill 工具的内部实现,持有对 *skill.Registry 的引用以
// 解析 skill_name 并读取完整 Skill 内容。
//
// 字段:
//
//	- registry:skill 注册表,只读使用,所有调用走 Get();空 registry 时 Get 返回
//	  (nil, false),Execute 直接返回「skill not found」错误。
//
// [Why] 持有 *Registry 而非 *Skill 列表:Skill 内容会随 SKILL.md 二次读盘而更新
// (FullContent 路径),Registry 始终指向最新数据;若改为缓存切片,SKILL.md 热更新
// 后 use_skill 仍返回旧内容,与 spec §C「按需加载」承诺相违。
type useSkillTool struct {
	registry *skill.Registry
}

// NewUseSkillTool 构造一个 use_skill 工具实例,供 main.go 在 tool.Registry 注册时使用。
//
// 参数:
//
//	- r:已通过 skill.LoadAll 装配好的 Registry;为 nil 时构造的工具仍可注册,
//	  但所有 Execute 调用都会返回「skill not found」错误(spec §非功能要求 6
//	  「零 Skill 启动兼容」场景)。
//
// 返回值:tool.Tool 接口(实际类型为 *useSkillTool)。
//
// [Why] 返回 tool.Tool 接口而非 *useSkillTool:与 builtin 包其他工具的构造函数
// 风格一致(NewReadFileTool 返回 *ReadFileTool 后由调用方转为 tool.Tool);
// 此处直接返回接口便于 main.go 一行 .Register(NewUseSkillTool(reg)) 完成注册。
func NewUseSkillTool(r *skill.Registry) tool.Tool {
	return &useSkillTool{registry: r}
}

// Name 返回工具名 "use_skill"。
//
// 实现 tool.Tool 接口。
func (t *useSkillTool) Name() string {
	return UseSkillName
}

// Description 返回工具的用途说明(详见 useSkillDescription 常量注释)。
//
// 实现 tool.Tool 接口。
func (t *useSkillTool) Description() string {
	return useSkillDescription
}

// InputSchema 返回 JSON Schema,供 LLM 理解入参结构。
//
// 实现 tool.Tool 接口。
func (t *useSkillTool) InputSchema() json.RawMessage {
	return useSkillInputSchema
}

// Permission 返回工具的权限分级(只读,与 ReadFile / Grep 同级)。
//
// 实现 tool.Tool 接口。
//
// [Why] 标注为 PermRead:use_skill 只读取 Registry 中缓存的 Skill 内容,不会
// 修改文件系统 / 不启动子进程;permission.Decide 默认对 PermRead 工具放行,
// 无需 setting.json 配置即可调用(spec §D.3)。
func (t *useSkillTool) Permission() tool.ToolPermission {
	return tool.PermRead
}

// Execute 实现 tool.Tool.Execute,按 skill_name 加载 Skill 完整内容。
//
// 行为(spec §D.2):
//
//   - ctx 已取消 → 立即返回 ctx.Err(),不访问 registry;
//   - input 解析失败 → 返回 error("参数解析失败: ...")由 ToolHandler 包装为 IsError=true;
//   - skill_name 为空字符串 → 返回 error("skill_name is required");
//   - skill_name 在 registry 中不存在 → 返回 error("skill not found: <name>");
//   - 找到 → 调 skill.FullContent() 返回完整 SKILL.md 内容(含重组后的 frontmatter +
//     正文;若 SKILL.md 已被外部修改,反映最新内容);
//
// 参数:
//
//	- ctx:支持通过 cancel 终止;使用前先检查 ctx.Err() 避免无效 registry 访问;
//	- input:LLM 传入的 JSON 字节,内部反序列化为 useSkillInput;
//
// 返回值:
//
//	- 成功:skill.FullContent() 的完整内容字符串,直接作为 tool_result 返回;
//	- 失败:("", error) 由 ToolHandler 包装为 ToolResultBlock{IsError: true}。
//
// 并发:registry 内部使用 sync.RWMutex 保护,Get 调用安全;多个 use_skill 并发
// 调用彼此互不阻塞(读路径)。
func (t *useSkillTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	// 1. ctx 取消检查:先于任何 IO/参数解析,避免无效工作(spec §D.2)。
	if err := ctx.Err(); err != nil {
		return "", err
	}

	// 2. 参数解析
	var in useSkillInput
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("参数解析失败: %w", err)
	}

	// 3. skill_name 必填校验(spec §D.1 + checklist 3.6)
	name := strings.TrimSpace(in.SkillName)
	if name == "" {
		return "", errors.New("skill_name is required")
	}

	// 4. ctx 二次检查(参数解析可能耗时,确保长时间运行的场景也能响应取消)
	if err := ctx.Err(); err != nil {
		return "", err
	}

	// 5. 查找 Skill(spec §D.2):项目级 → 用户级 → 内置级优先级由 Registry.Register
	// 加载顺序保证;Get 直接按 name 取最后一次 Register 成功的 Skill。
	s, ok := t.registry.Get(name)
	if !ok || s == nil {
		return "", fmt.Errorf("skill not found: %s", name)
	}

	// 6. FullContent:二次读盘反映 SKILL.md 最新内容;失败时 error 由 ToolHandler
	// 包装为 IsError=true(spec §D.2)。Body() 是缓存版本,本工具选择 FullContent
	// 保证「最新」,因为 use_skill 是 LLM 主动调用路径(非高频),一次磁盘读可接受。
	content, err := s.FullContent()
	if err != nil {
		return "", err
	}
	return content, nil
}