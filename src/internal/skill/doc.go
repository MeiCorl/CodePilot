// Package skill 是 CodePilot Skill 系统的「基础数据层」。
//
// Skill 是一种「目录型」能力模块:每个 Skill 是一个目录,主入口为 SKILL.md,
// 内含 YAML frontmatter(name / description / 可选 args / 可选 allowed-tools)
// 与 markdown 正文。本包只关心 SKILL.md 的数据形态与解析,不涉及:
//   - 多档扫描(项目级 / 用户级 / 内置级)与三档合并冲突规则——见 sub-package registry
//     与 scanner(后续 Task 2 落地);
//   - Skill → slash 命令 / use_skill 工具 / prompt Source 的适配层——见 sub-package adapter
//     与 sources(后续 Task 3~5 落地);
//   - UI 呈现(/skills 列表 / 紫色 skill 徽标)与主流程装配——见 Task 6~7。
//
// 本包主要导出:
//   - Source:iota 枚举,标记 Skill 来源级别(项目级 / 用户级 / 内置级);
//   - Skill:Skill 数据结构,持有 SKILL.md 的 frontmatter + markdown 正文与 RootPath;
//   - Skill.Body():返回重组后的完整 markdown(含 # Skill 标题 + 描述 + 正文);
//   - Skill.FullContent():按 RootPath/SKILL.md 重新读盘后组装(用于运行期拿最新内容)。
//
// 子包 loader 提供 SKILL.md 文件的解析器(ParseFile),是 Skill 数据流入的入口。
//
// 架构定位:本包归属 CodePilot 第 3 层「工具层」,作为可插拔能力模块,
// 与 tool.Tool / slash.SlashCommand 同层;不依赖 web / handler 等上层模块。
package skill
