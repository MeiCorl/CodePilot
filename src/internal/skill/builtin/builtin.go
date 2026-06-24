// Package builtin 是 Skill 系统的「内置级 Skill」目录。
//
// 用途:
//   - 本步骤 CodePilot 自身不内置任何 Skill,该目录作为预留扩展点;
//   - 后续 Step 11 / 12 或 CodePilot 官方版本可在 <exec>/internal/skill/builtin/
//     下放置官方 Skill(如 codepilot-init / codepilot-commit 等),
//     走与项目级 / 用户级同构的 SKILL.md 格式即可被自动识别;
//
// 设计要点(spec §A.1):
//   - 本包是「数据存放 + 路径常量声明」包,不实现扫描逻辑;
//   - 扫描器实现在 src/internal/skill/scanner.go 的 scanBuiltinLevel(),
//     与 user / project 档走相同的 loader.ParseFile 路径;
//   - 这样的目的是避免循环依赖:
//     -- skill 主包不能 import builtin(否则 builtin.ScanBuiltin → skill.Skill → 循环);
//     -- builtin 包作为「目录 + 常量 + 占位代码」独立存在,不依赖 skill 主包;
//   - 后续真正内置 Skill 时,只需在 builtin/<name>/SKILL.md 放置文件即可,无需
//     修改 scanner.go / builtin.go 中的任何代码。
package builtin

// DirName 是内置级 Skill 目录相对于 execDir 的子路径常量。
//
// [Why] 常量化:与 spec §A.1 的「<exec>/internal/skill/builtin/<skill-name>/SKILL.md」
// 描述保持一一对应,避免硬编码散落;scanner.scanBuiltinLevel 也引用此常量,
// 保证两侧行为一致。
//
// 完整路径 = filepath.Join(execDir, "internal", "skill", builtin.DirName)。
const DirName = "builtin"
