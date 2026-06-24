# 内置 Skill 目录(占位)

本目录为 CodePilot Skill 系统的「内置级 Skill」扩展点,当前为空。

## 设计意图

按 spec §A.1,CodePilot 自身具备三档 Skill 优先级:

| 优先级 | 路径                                  | 说明                            |
| --- | ----------------------------------- | ----------------------------- |
| 最高  | `<cwd>/.codepilot/skills/<name>/SKILL.md` | 项目级,跟随项目分发                       |
| 中    | `~/.codepilot/skills/<name>/SKILL.md`     | 用户级,跨项目生效                      |
| 最低  | `<exec>/internal/skill/builtin/<name>/SKILL.md` | 内置级,CodePilot 自带分发(预留扩展点) |

## 当前状态

Step 10 实现期内,CodePilot 不内置任何 Skill,本目录仅保留:

- `builtin.go`:`ScanBuiltin(execDir)` 函数,恒定返回空切片;
- `README.md`(本文件):占位说明。

## 扩展方式(后续步骤)

后续若需添加官方内置 Skill(例如 `codepilot-init`、`codepilot-commit` 等),只需在本目录下创建子目录并按 SKILL.md 约定编写文件即可。约定:

1. 子目录名 = Skill 名(用于 `name:` 字段或目录名,两者需一致);
2. 子目录下必须有 `SKILL.md`,YAML frontmatter 包含 `name` / `description`(必填),
   可选 `args` / `allowed-tools`;
3. 扩展后 `ScanBuiltin` 将自动扫描到这些 Skill,无需修改 Scanner 或 main.go。

## 与 spec §A.5 加载失败隔离的兼容

若某个内置 SKILL.md 解析失败,YAML 错误或缺 frontmatter 等,`ScanBuiltin` 返回
`(nil, error)`,由 Scanner 调用方决定如何处理。建议与 user / project 级一致:
解析失败 → 记录 `LoadIssue` + 跳过该 Skill,不影响其他 Skill 加载与程序启动。
