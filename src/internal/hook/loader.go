// Package hook — LoadFromConfig 入口 (Task 5 §3 + spec §非功能要求 3)。
//
// LoadFromConfig 把 config.HookConfig(经 applyHookDefaults + ValidateHookConfig
// 校验过的)一次性转成 Engine 实例,是 main.go 装配 hook 系统的唯一入口。
//
// 设计要点:
//   - 入口函数而非包级:避免在测试中与 main.go 共享同一 Engine 实例;
//   - Enabled=false 路径:仍返回 Engine 实例(cfg.Enabled=false),但 entries
//     为空,Dispatch 走 no-op fast-path(spec §非功能要求 3「零配置安全降级」);
//   - 校验:Event 合法性 + Action.Type 合法性 + executor 构造 + condition
//     解析;每条 entry 独立校验,任一失败 → 整体 error(LoadEntries 内部已
//     走同一校验,这里仅做 Enabled 早返回);
//   - 与 EngineConfig 分离:Loader 只关心「如何把 HookConfig 转 entries」,
//     Engine 关心的「如何 wire 依赖」由调用方(LoadFromConfig 的 caller)
//     构造 EngineConfig 后传入。
package hook

import (
	"github.com/MeiCorl/CodePilot/src/internal/config"
)

// LoadFromConfig 从 config.HookConfig 构造 Engine 实例并注册所有 entries。
//
// 流程:
//  1. 构造 Engine(把 cfg 透传);
//  2. 调 engine.LoadEntries(hookCfg.Entries) 注册;
//  3. 返回 engine / error。
//
// 任何一条 entry 校验/构造失败时,LoadEntries 已整体回滚(不写入 entries map),
// LoadFromConfig 仍返回 error 让 caller 决定是否 fail-fast,但 engine 实例
// 仍返回(保持调用方对 cfg 状态的可见性)。
//
// 参数:
//   - hookCfg:已通过 config.ValidateHookConfig 校验的 Hook 配置(可为零值);
//   - engineCfg:Engine 运行期依赖(Logger / LLMProvider / ToolRegistry / PromptSink);
//
// 返回:配置有效时返回非 nil Engine;配置无效时返回 engine(空 entries) + error。
//
// [Why 校验不在 LoadFromConfig 重复做] config.ValidateHookConfig 已在
// config.loadAndMerge → c.validate() 阶段把「必须项」校验过(Name / Event /
// Action.Type);LoadFromConfig 把重心放在「构造 executor + 解析 condition」,
// 这些是 hook 包专属逻辑,config 包不感知。
func LoadFromConfig(hookCfg *config.HookConfig, engineCfg EngineConfig) (*Engine, error) {
	// 1) 构造 Engine(把 cfg 透传)
	engine := New(engineCfg)

	// 2) 注册 entries(LoadEntries 内已做整体回滚 + 校验,这里只透传)
	if hookCfg == nil {
		return engine, nil
	}
	if err := engine.LoadEntries(hookCfg.Entries); err != nil {
		// 失败时仍返回 engine(空 entries),便于 caller 调用 Shutdown 等
		return engine, err
	}
	return engine, nil
}
