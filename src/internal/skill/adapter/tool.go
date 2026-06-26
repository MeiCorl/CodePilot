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

	skillbuiltin "github.com/MeiCorl/CodePilot/src/internal/skill/builtin"

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
//   - registry:skill 注册表,只读使用,所有调用走 Get();空 registry 时 Get 返回
//     (nil, false),Execute 直接返回「skill not found」错误;
//   - rootBySource:三档 Skill(builtin / user / project)对应的「实际可读文件系统根目录」
//     绝对路径。在 use_skill 返回时,会用它前置一段 XML 路径提示,告诉 LLM 这个 Skill
//     所属 source 的真实绝对路径(便于 LLM 用 ReadFile 拼出 reference/*.md 等子文档路径)。
//     为空时,Execute 跳过路径提示(零 Skill / 早期降级场景)。
//
// [Why 持有 *Registry 而非 *Skill 列表] Skill 内容会随 SKILL.md 二次读盘而更新
// (FullContent 路径),Registry 始终指向最新数据;若改为缓存切片,SKILL.md 热更新
// 后 use_skill 仍返回旧内容,与 spec §C「按需加载」承诺相违。
//
// [Why rootBySource 单独传,而不是从 Skill.RootPath 取] Skill.RootPath 对
// embedded builtin skill 是「embedded://internal/skill/builtin/<name>」这种
// 虚拟路径(见 scanner.scanEmbeddedBuiltins),无法被 ReadFile 沙箱识别;
// 真实可读的是 dist 副本路径(<execDir>/internal/skill/builtin/)或 workdir fallback
// 副本路径(<workdir>/src/internal/skill/builtin/)。use_skill 必须把这种
// 「Skill 元数据路径」与「真实可读文件系统路径」分离,才能让 LLM 拼出正确的 ReadFile 路径。
type useSkillTool struct {
	registry     *skill.Registry
	rootBySource map[skill.Source]string
}

// NewUseSkillTool 构造一个 use_skill 工具实例,供 main.go 在 tool.Registry 注册时使用。
//
// 参数:
//
//   - r:已通过 skill.LoadAll 装配好的 Registry;为 nil 时构造的工具仍可注册,
//     但所有 Execute 调用都会返回「skill not found」错误(spec §非功能要求 6
//     「零 Skill 启动兼容」场景)。
//   - rootBySource:三档 Skill(builtin / user / project)的「实际可读文件系统根目录」
//     绝对路径;为 nil 或缺少某档时,Execute 在路径提示里只列已有的档位,绝不阻塞返回。
//
// 返回值:tool.Tool 接口(实际类型为 *useSkillTool)。
//
// [Why 返回 tool.Tool 接口而非 *useSkillTool] 与 builtin 包其他工具的构造函数
// 风格一致(NewReadFileTool 返回 *ReadFileTool 后由调用方转为 tool.Tool);
// 此处直接返回接口便于 main.go 一行 .Register(NewUseSkillTool(reg, roots)) 完成注册。
func NewUseSkillTool(r *skill.Registry, rootBySource map[skill.Source]string) tool.Tool {
	return &useSkillTool{registry: r, rootBySource: rootBySource}
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
//   - ctx:支持通过 cancel 终止;使用前先检查 ctx.Err() 避免无效 registry 访问;
//   - input:LLM 传入的 JSON 字节,内部反序列化为 useSkillInput;
//
// 返回值:
//
//   - 成功:skill.FullContent() 的完整内容字符串,直接作为 tool_result 返回;
//   - 失败:("", error) 由 ToolHandler 包装为 ToolResultBlock{IsError: true}。
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

	// 7. 前置「Skill 根路径提示」(Step 10.2 Bugfix):Skill 索引类(如 codebase-overview)
	//    让 LLM 用 ReadFile 读 reference/*.md 等子文档,但 LLM 不知道 Skill 在文件系统的
	//    实际绝对路径(尤其是 builtin Skill 的 RootPath 是 embedded:// 虚拟路径,
	//    ReadFile 沙箱无法识别)。这里前置一段 XML 注释形式的路径提示,把当前 Skill 所属
	//    Source 对应的「实际可读文件系统根」告诉 LLM,LLM 据此拼出 ReadFile 完整绝对路径。
	//
	//    [Why 不直接拼绝对路径写进 SKILL.md] SKILL.md 是静态资源,无法在每次启动时
	//    注入实际绝对路径;且不同部署方式(dist / dev / project)实际路径不同。
	//    use_skill 工具是注入动态信息的唯一合规出口。
	//
	//    [Why XML 注释而非纯文本] LLM 会忽略 HTML/XML 注释,但人类调试 use_skill 输出时
	//    可以清楚区分「提示段」与「Skill 主体内容」;同时 XML 注释不会破坏 markdown 渲染。
	hint := t.buildRootHint(s)
	if hint == "" {
		return content, nil
	}
	return hint + content, nil
}

// buildRootHint 生成 use_skill 返回时的「Skill 根路径提示」XML 注释段。
//
// 行为:
//   - rootBySource 为 nil 或全空 → 返回 ""(不前置任何内容,避免无意义的提示污染);
//   - 当前 Skill 所属 Source 在 rootBySource 中有路径 → 提示该 Source 的实际可读根目录;
//   - 当前 Skill 所属 Source 在 rootBySource 中缺失 → 提示「无可读路径,仅可看 SKILL.md 内容」。
//
// 返回格式示例(SourceBuiltin 命中时):
//
//	<!--
//	[CodePilot Skill 根路径提示 — Step 10.2 Bugfix 重做]
//	本 Skill 名称 = codebase-overview
//	本 Skill 来源 = builtin
//	本 Source 实际可读文件系统根 = D:\CodePilot\build\dist\internal\skill\builtin
//
//	【读取该 Skill 子文档的方式(务必按此拼路径)】
//	ReadFile 工具的 file_path 参数必须是完整绝对路径,不要保留任何占位符。
//	拼路径模板: <上面那行的根路径> + 反斜杠 + <Skill 名> + 反斜杠 + <子文件相对路径>
//
//	示例 — 读取本 Skill 的 reference 子文档时,file_path 必须长这样(以 codebase-overview 为例):
//	  拼路径: D:\CodePilot\build\dist\internal\skill\builtin\codebase-overview\reference\context-management.md
//	  拼路径: D:\CodePilot\build\dist\internal\skill\builtin\codebase-overview\reference\permission.md
//	  拼路径: D:\CodePilot\build\dist\internal\skill\builtin\codebase-overview\reference\tool-system.md
//
//	【禁止事项 — 上一版提示在此处翻车,务必不要犯同样的错】
//	- 不要保留任何 <...> / {...} 占位符(如 <module>、{filename}、Skill 名等字面文本)
//	- 不要用 find / ls / dir 等 Bash 命令搜索子文档
//	- 不要把根路径与子文件路径用 "+" 拼接写进 tool input — tool input 只接 file_path 一个完整字符串
//	- 拼路径时,参考本 Skill 的 SKILL.md 正文「模块索引」表里列出的子文件名,把它们填到上面的 <子文件相对路径> 位置
//
//	【沙箱放行确认】这个根目录已经被沙箱放行为 ReadFile 附加只读根,ReadFile 调用不会被路径限制拦截,可以放心使用绝对路径。
//	-->
//
//	# Skill: codebase-overview
//	...
//
// [Why 注释形式] LLM 渲染 markdown 时不会看到注释,但会读注释里的指令;
// 同时注释段不破坏 SKILL.md 原本的 markdown 结构与 frontmatter。
//
// [Why 给 3 个具体真实路径示例] 上一版提示用 `ReadFile("<root>\\<skill>\\reference\\<module>.md")`
// 这种「伪代码表达式 + 占位符」格式,实测 LLM 会把 `<module>` 当字面拼进 file_path,
// 直接导致 ReadFile 失败。新版改为「3 条 100% 完整可复制的真实路径」,LLM 只需替换文件名
// 段就能拿到正确路径,绝不误读。
func (t *useSkillTool) buildRootHint(s *skill.Skill) string {
	if t.rootBySource == nil || len(t.rootBySource) == 0 {
		return ""
	}
	root, ok := t.rootBySource[s.Source]
	if !ok || root == "" {
		if skillbuiltin.IsEmbeddedPath(s.RootPath) {
			return t.buildEmbeddedRootHint(s)
		}
		var sb strings.Builder
		sb.WriteString("<!--\n")
		sb.WriteString("[CodePilot Skill 根路径提示]\n")
		sb.WriteString(fmt.Sprintf("本 Skill 名称 = %s\n", s.Name))
		sb.WriteString(fmt.Sprintf("本 Skill 来源 = %s\n", s.Source))
		sb.WriteString("本 Source 实际可读路径不可用,未找到 filesystem 副本。\n")
		sb.WriteString("如需读取该 Skill 子文档,请检查 CodePilot 部署目录 dist 或项目 src 是否有 SKILL.md 副本；否则仅基于 SKILL.md 正文回答。\n")
		sb.WriteString("-->\n\n")
		return sb.String()
	}
	var sb strings.Builder
	sb.WriteString("<!--\n")
	sb.WriteString("[CodePilot Skill 根路径提示 — Step 10.2 Bugfix 重做]\n")
	sb.WriteString(fmt.Sprintf("本 Skill 名称 = %s\n", s.Name))
	sb.WriteString(fmt.Sprintf("本 Skill 来源 = %s\n", s.Source))
	sb.WriteString(fmt.Sprintf("本 Source 实际可读文件系统根 = %s\n", root))
	sb.WriteString("\n")
	sb.WriteString("【读取该 Skill 子文档的方式 — 直接复制下面的真实路径示例】\n")
	sb.WriteString("ReadFile 工具的 file_path 参数必须是完整绝对路径,不能含有任何尖括号或花括号占位符。\n")
	sb.WriteString("直接复制下面的示例路径,只把最后那段文件名替换成你想读的实际子文档名,其它部分一字不动:\n")
	sb.WriteString("\n")
	sb.WriteString(fmt.Sprintf("  %s\\%s\\reference\\context-management.md\n", root, s.Name))
	sb.WriteString(fmt.Sprintf("  %s\\%s\\reference\\permission.md\n", root, s.Name))
	sb.WriteString(fmt.Sprintf("  %s\\%s\\reference\\tool-system.md\n", root, s.Name))
	sb.WriteString("\n")
	sb.WriteString("【禁止事项 — 上一版提示在此处翻车,务必不要犯同样的错】\n")
	sb.WriteString("- 不要写任何形式的占位符表达式(尖括号里夹文字、花括号里夹文字、Skill 名做变量等)\n")
	sb.WriteString("- 不要把多个路径段用 + 号拼接写进 tool input — tool input 只接 file_path 一个完整字符串\n")
	sb.WriteString("- 不要用 find / ls / dir 等 Bash 命令搜索子文档(本 Skill 的 SKILL.md 模块索引表已经列出全部子文件名)\n")
	sb.WriteString("- 复制示例路径后,只需把最后那段 reference 后的文件名替换成「模块索引」表里的目标子文档名\n")
	sb.WriteString("\n")
	sb.WriteString("【沙箱放行确认】这个根目录已经被沙箱放行为 ReadFile 附加只读根,ReadFile 调用不会被路径限制拦截,可以放心使用绝对路径。\n")
	sb.WriteString("-->\n\n")
	return sb.String()
}
func (t *useSkillTool) buildEmbeddedRootHint(s *skill.Skill) string {
	var sb strings.Builder
	sb.WriteString("<!--\n")
	sb.WriteString("[CodePilot Skill 内置资源路径提示]\n")
	sb.WriteString(fmt.Sprintf("本 Skill 名称 = %s\n", s.Name))
	sb.WriteString(fmt.Sprintf("本 Skill 来源 = %s\n", s.Source))
	sb.WriteString(fmt.Sprintf("本 Source 内置资源根 = %s\n", skillbuiltin.EmbeddedRoot))
	sb.WriteString("\n")
	sb.WriteString("【读取该 Skill 子文档的方式 - 直接复制下面的 embedded 路径示例】\n")
	sb.WriteString("ReadFile 工具的 file_path 参数可以使用 embedded:// 内置 Skill 路径。下面三条是完整真实路径,不要加入尖括号、花括号或加号拼接。\n")
	sb.WriteString("\n")
	sb.WriteString(fmt.Sprintf("  %s\n", skillbuiltin.EmbeddedPath(s.Name, "reference/context-management.md")))
	sb.WriteString(fmt.Sprintf("  %s\n", skillbuiltin.EmbeddedPath(s.Name, "reference/permission.md")))
	sb.WriteString(fmt.Sprintf("  %s\n", skillbuiltin.EmbeddedPath(s.Name, "reference/tool-system.md")))
	sb.WriteString("\n")
	sb.WriteString("【禁止事项】不要使用占位符;不要使用 Bash 搜索;不要把路径片段拼接进 tool input。复制示例路径后,只替换最后的文件名。\n")
	sb.WriteString("【沙箱放行确认】embedded://internal/skill/builtin/ 已作为 ReadFile 只读内置资源路径放行。\n")
	sb.WriteString("-->\n\n")
	return sb.String()
}
