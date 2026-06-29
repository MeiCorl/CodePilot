## §6 skill — Skill 系统配置

### 路径说明

`skill` 段控制 Step 10 Skill 系统的总开关与单 Skill 正文截断上限。

### JSON schema 摘要

```jsonc
{
  "skill": {
    "enabled": true,                      // 总开关,*bool,nil 视为 true
    "max_skill_size_bytes": 65536         // 单 SKILL.md 正文截断阈值(字节)
  }
}
```

### 完整示例

```json
{
  "skill": {
    "enabled": true,
    "max_skill_size_bytes": 65536
  }
}
```

### 字段默认值与单位

| 字段 | 默认 | 单位 | 说明 |
|------|------|------|------|
| `enabled` | true | — | `false` 时 main.go 完全跳过 Skill 加载(不调 LoadAll、不注册 use_skill 工具、不注入 SkillsIndexSource) |
| `max_skill_size_bytes` | 65536 (64 KB) | 字节 | 单 SKILL.md 正文超过此值将被截断(避免 use_skill 工具返回撑爆上下文) |

### 是否需要重启

**需要重启**。`enabled` 影响 main.go 启动期装配逻辑。

### 错误排查

- 设 `enabled: false` 后 WebUI `/skills` 仍可见列表 → 确认已重启;该开关在启动期生效;
- Skill 被截断 → 调大 `max_skill_size_bytes`,或精简 Skill 正文(推荐);
- 注意:`enabled: false` 时,**config-management Skill 也不可用**,但 SP 自描述段
  仍会引导 Agent 知道「配置文件在哪」(指向 Skill 不可用是已知降级)。

---
