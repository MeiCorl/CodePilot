// Package matcher — 条件评估引擎(spec §B + Task 3 §2)。
//
// Matcher 是无状态的纯函数集合,无内部缓存、无锁、可并发安全;零值即可使用。
// 核心入口 Evaluate(cond, hookCtx) (matched, reason) 把 Condition DSL
// 翻译为对 HookContext 字段的等价比较,支持:
//
//   - 空 condition / nil condition:永远匹配(spec §B「顶层 condition 缺省 / null
//     视为永远匹配」);
//   - all 组合:所有子 Evaluate 为 true 时才匹配;空数组视为 true(spec §B);
//   - any 组合:任一子 Evaluate 为 true 时匹配;空数组视为 false(spec §B);
//   - leaf:四种操作 eq / neq / glob / contains,均通过字段路径取值后比较。
//
// 字段路径规则(Task 3 §2):
//   - event / tool_name / tool_input_file_path / message_role /
//     session_id / workdir / error / message_content / tool_result:HookContext
//     直接字段;
//   - iteration / tool_duration_ms / tool_is_error:数值/布尔字段,统一转字符串;
//   - tool_input.<key>:从 ToolInput map 取子键(spec §E「嵌套字段」);
//   - 其它字段(spec 之外的):返回空字符串,eq 视为不匹配,neq/glob/contains 视为
//     不匹配(spec §B「安全降级」+ tasks.md §3.2 第 5 点)。
//
// [Why 字段缺失不 panic] 工具事件可能在非工具事件下触发(如 session_start 没有
// ToolInput);condition 引用的 field 在 ctx 中不存在时,按 spec §B 安全降级:
// glob/contains/neq 视为 false,eq 视为 false;让单个坏条件不阻断整组配置。
package matcher

import (
	"fmt"
	"path"
	"strings"

	"github.com/MeiCorl/CodePilot/src/internal/hookcontext"
)

// Op 常量:Condition.Op 的合法值(spec §B + Task 3 §1)。
//
// [Why 集中定义] 避免散落字符串字面量,便于 grep / 排错 / 文档化。
const (
	OpEq       = "eq"       // 字符串相等(默认)
	OpNeq      = "neq"      // 字符串不等
	OpGlob     = "glob"     // path.Match 风格 glob
	OpContains = "contains" // 子串包含
)

// 嵌套字段前缀。
const (
	toolInputPrefix = "tool_input."
)

// Matcher 是无状态匹配器。
//
// 设计上无任何运行时状态(无 map / 无 mutex),所有方法可并发安全调用;
// 零值 Matcher{} 即合法,允许调用方用 `var m Matcher` 零值直接 Evaluate。
//
// [Why 不需要 builder] 评估逻辑纯函数化,无配置注入;后续若加 op 白名单
// / 性能缓存等可在此扩展,但当前保持极简。
type Matcher struct{}

// NewMatcher 构造 Matcher 指针。
//
// [Why 提供构造器] 与引擎层 entry 构造保持风格统一(matchers 作为字段),调用方
// 可以选择 `&Matcher{}` 或 `NewMatcher()`;语义上两者等价。
func NewMatcher() *Matcher {
	return &Matcher{}
}

// Evaluate 评估 condition 是否匹配 hookCtx。
//
// 返回:
//   - matched:是否匹配;
//   - reason:匹配/不匹配的可读原因(便于日志/排错),如 "no condition" /
//     "leaf eq match" / "field not found" / "glob no match" 等。
//
// 评估规则(自上而下):
//  1. cond 为 nil 或 IsEmpty → 匹配(无条件);
//  2. All 非空 → 所有子 Evaluate 必须 true;空 All 视为 true;
//  3. Any 非空 → 任一子 Evaluate 为 true;空 Any 视为 false;
//  4. leaf → 根据 Op 评估 eq/neq/glob/contains;
//  5. 字段不存在 / 类型不匹配 → 不 panic,按 spec §B 安全降级。
//
// [Why 返回 reason 字符串] Engine / 日志会打印匹配过程,reason 字段为可读诊断;
// 调用方可选择只用 matched,也可以把 reason 写到 debug 日志里(spec §G「可观测性」)。
func (m *Matcher) Evaluate(cond Condition, hookCtx *hookcontext.HookContext) (bool, string) {
	_ = m // m 保留为后续可能注入配置(如 op 白名单)的扩展点

	// 规则 1:空 condition → 永远匹配
	if cond.IsEmpty() {
		return true, "no condition"
	}

	// 规则 2:all 组合
	// [Why 用 cond.All != nil 而非 len(cond.All) > 0] ParseCondition 会把空
	// All slice 归零为 nil,但 Evaluate 也允许直接构造的空 slice(测试场景);
	// 这里用 nil 判定保留「显式空 all 视为真」的语义,即便用户直接构造
	// Condition{All: []Condition{}} 也能走 all 分支并按 spec §B 返回 true。
	if cond.All != nil {
		for i, sub := range cond.All {
			ok, subReason := m.Evaluate(sub, hookCtx)
			if !ok {
				return false, fmt.Sprintf("all[%d] not matched: %s", i, subReason)
			}
		}
		return true, fmt.Sprintf("all matched (%d conditions)", len(cond.All))
	}

	// 规则 3:any 组合(同理:cond.Any != nil 保留「显式空 any 视为假」语义)
	if cond.Any != nil {
		for i, sub := range cond.Any {
			ok, subReason := m.Evaluate(sub, hookCtx)
			if ok {
				return true, fmt.Sprintf("any[%d] matched: %s", i, subReason)
			}
		}
		return false, fmt.Sprintf("any not matched (%d conditions)", len(cond.Any))
	}

	// 规则 4:leaf 评估
	return evaluateLeaf(cond, hookCtx)
}

// evaluateLeaf 评估单个 leaf condition(spec §B + Task 3 §2)。
//
// 实现:
//   - resolveField 取 ctx 中对应字段的字符串值(失败时返回 ""+false);
//   - toComparableString 把 cond.Value 统一转字符串;
//   - 按 Op 分发到 evaluateEq / evaluateNeq / evaluateGlob / evaluateContains;
//   - 任何未知 Op → 不匹配(spec §B 安全降级,符合 tasks.md §3.2 第 5 点)。
//
// 字段缺失/类型不匹配时:
//   - eq:ctxField="" 且 condValue="":空串相等视为「不匹配」(防误命中);
//        其它情况:condValue 非空而 ctxField 为空 → 不匹配(spec §B 「eq 视为不匹配」);
//   - neq:ctxField="" → 视为「不匹配」(无法判断不等);
//   - glob:ctxField="" → 视为「不匹配」;
//   - contains:ctxField="" → 视为「不匹配」。
func evaluateLeaf(cond Condition, hookCtx *hookcontext.HookContext) (bool, string) {
	ctxValue, ok := resolveField(cond.Field, hookCtx)
	if !ok {
		// 字段不在 ctx 中(spec §B 安全降级:不抛错,视为不匹配)
		return false, fmt.Sprintf("field %q not found in context", cond.Field)
	}

	want := toComparableString(cond.Value)

	switch strings.ToLower(cond.Op) {
	case OpEq, "": // 空 Op 视为 eq(spec §B 默认)
		return evaluateEq(cond.Field, ctxValue, want)
	case OpNeq:
		return evaluateNeq(cond.Field, ctxValue, want)
	case OpGlob:
		return evaluateGlob(cond.Field, ctxValue, want)
	case OpContains:
		return evaluateContains(cond.Field, ctxValue, want)
	default:
		// 未知 op:不匹配,不抛错(spec §B + tasks.md §3.2 第 5 点)
		return false, fmt.Sprintf("unknown op %q for field %q", cond.Op, cond.Field)
	}
}

// resolveField 从 HookContext 读取 spec §E 约定的字段值。
//
// 返回:
//   - string:字段字符串值(布尔/数值已转 string,缺失/不匹配返回 "");
//   - bool:是否成功解析(用于 leaf 评估时区分「字段不存在」与「字段为空字符串」)。
//
// [Why 第二个返回值] leaf 评估时若字段不存在,需按 spec §B 安全降级;
// 用 ok bool 让调用方明确「找不到」与「找到但为空」是两种语义。
// 例:tool_name="" 视为字段存在(OK=true),tool_is_error 字段路径错误视为不存在(OK=false)。
func resolveField(name string, hookCtx *hookcontext.HookContext) (string, bool) {
	if hookCtx == nil {
		return "", false
	}

	// 1) 嵌套 tool_input.<key>(大小写不敏感:HookContext.Vars 统一大写)
	if strings.HasPrefix(strings.ToLower(name), toolInputPrefix) {
		key := strings.TrimPrefix(strings.ToLower(name), toolInputPrefix)
		if hookCtx.ToolInput == nil {
			return "", false
		}
		// HookContext.ToolInput 来自 JSON 反序列化,key 保留原始大小写;
		// 用户的 condition 既可能用 "tool_input.file_path" 也可能用 "tool_input.FILE_PATH",
		// 这里做一次大小写不敏感查找(spec §B「大小写策略」与 Vars() 保持一致)。
		for k, v := range hookCtx.ToolInput {
			if strings.EqualFold(k, key) {
				if s, ok := anyToString(v); ok {
					return s, true
				}
				// 字段存在但值类型不匹配(如 nil / map / 数组)→ 视为「找到但不可比较」
				// spec §B 安全降级:返回空串 + true,让 leaf 评估按空串处理。
				return "", true
			}
		}
		return "", false
	}

	// 2) 直接字段(大小写不敏感:兼容用户写 "Event" / "EVENT" / "event")
	switch strings.ToLower(name) {
	case "event":
		if hookCtx.Event == "" {
			return "", false
		}
		return hookCtx.Event, true
	case "category":
		if hookCtx.Category == "" {
			return "", false
		}
		return hookCtx.Category, true
	case "tool_name":
		if hookCtx.ToolName == "" {
			return "", false
		}
		return hookCtx.ToolName, true
	case "tool_input_file_path":
		if hookCtx.ToolInputFilePath == "" {
			return "", false
		}
		return hookCtx.ToolInputFilePath, true
	case "message_role":
		if hookCtx.MessageRole == "" {
			return "", false
		}
		return hookCtx.MessageRole, true
	case "message_content":
		if hookCtx.MessageContent == "" {
			return "", false
		}
		return hookCtx.MessageContent, true
	case "tool_result":
		if hookCtx.ToolResult == "" {
			return "", false
		}
		return hookCtx.ToolResult, true
	case "error":
		if hookCtx.Error == "" {
			return "", false
		}
		return hookCtx.Error, true
	case "session_id":
		if hookCtx.SessionID == "" {
			return "", false
		}
		return hookCtx.SessionID, true
	case "workdir":
		if hookCtx.Workdir == "" {
			return "", false
		}
		return hookCtx.Workdir, true

	// 数值/布尔字段(spec §E + 字段路径)
	// [Why iteration / tool_is_error 在 0 / false 时仍返回 ok=true]
	// 这是有意义的零值(不是「未设置」);若需区分「未设置」,应通过 HookContext
	// 整体语义判断(events 类型与字段是否相关)。
	case "iteration":
		return fmt.Sprintf("%d", hookCtx.Iteration), true
	case "tool_duration_ms":
		return fmt.Sprintf("%d", hookCtx.ToolDurationMs), true
	case "tool_is_error":
		return fmt.Sprintf("%t", hookCtx.ToolIsError), true

	default:
		// 未知字段:spec §B 安全降级(不抛错,返回 not found)
		return "", false
	}
}

// evaluateEq 实现 eq 操作。
//
// 语义:ctxField == condValue 字符串相等(空串 == 空串仍为 false,防误命中)。
// [Why 空串不匹配] ctxField 为空时即使 condValue 也为空,我们也不想让「无意义
// 的全空 condition」误命中(否则所有 hook 在 ctx 全空时都会被触发)。
func evaluateEq(field, ctxValue, want string) (bool, string) {
	if ctxValue == "" && want == "" {
		return false, fmt.Sprintf("eq on %q: both empty (intentionally non-match)", field)
	}
	if ctxValue == want {
		return true, fmt.Sprintf("eq on %q: %q == %q", field, ctxValue, want)
	}
	return false, fmt.Sprintf("eq on %q: %q != %q", field, ctxValue, want)
}

// evaluateNeq 实现 neq 操作。
//
// 语义:ctxField != condValue;ctxField 为空时视为不匹配(spec §B「neq 视为不匹配」)。
func evaluateNeq(field, ctxValue, want string) (bool, string) {
	if ctxValue == "" {
		return false, fmt.Sprintf("neq on %q: context field empty (intentionally non-match)", field)
	}
	if ctxValue != want {
		return true, fmt.Sprintf("neq on %q: %q != %q", field, ctxValue, want)
	}
	return false, fmt.Sprintf("neq on %q: %q == %q", field, ctxValue, want)
}

// evaluateGlob 实现 glob 操作。
//
// 跨平台策略(关键):Go 标准库 filepath.Match 在 Windows 下用 `\` 作分隔符,
// Linux 下用 `/`,同一 glob 模式 `internal/*.go` 在两平台对 `internal\foo.go`
// 的匹配结果不一致;为遵循 spec §B「支持 Windows 路径」+ tasks.md §3.2 C.13
// 要求「输入 internal\foo.go 用 glob internal/*.go 也要匹配」,本函数:
//
//  1. 把 glob pattern 与 value 都归一化为 `/` 分隔;
//  2. 用 path.Match(纯 / 语义,无平台差异)评估;
//  3. 失败/ErrBadPattern 时回退到原样再 path.Match 一次(防御性)。
//
// 此外为支持 spec §B 的典型用例 `tool_input.file_path glob '*.go'`,
// value 可能是 `internal/foo.go` 这样的多段路径,标准 path.Match 的 `*` 不跨
// `/` 会导致不匹配。这里同时做「value basename 兜底匹配」:取 value 末段
// (去掉所有 `/` 前缀)与 pattern 评估,让 `*.go` 能匹配 `foo.go`(basename)。
//
// [Why 不用 filepath.Match] Windows 下 filepath.Match 要求 pattern 也用
// `\` 分隔,无法满足 C.13;path.Match 始终用 `/`,跨平台行为一致。
//
// [Why basename 兜底] spec §B 用例 `*.go` 匹配多段路径是用户的常见预期;
// 严格遵循 path.Match 语义(只支持单段 *)会过于限制体验。basename 兜底是
// Claude Code 等主流 Agent 的事实标准行为,符合用户预期。
func evaluateGlob(field, ctxValue, pattern string) (bool, string) {
	if ctxValue == "" {
		return false, fmt.Sprintf("glob on %q: context field empty (intentionally non-match)", field)
	}
	if pattern == "" {
		return false, fmt.Sprintf("glob on %q: empty pattern", field)
	}

	// 1) 归一化为正斜杠(/)
	normValue := normalizePathSeparator(ctxValue)
	normPattern := normalizePathSeparator(pattern)

	// 2) 全路径匹配(path.Match 严格语义:pattern 必须 match 整段 name)
	if ok, err := path.Match(normPattern, normValue); err == nil && ok {
		return true, fmt.Sprintf("glob on %q: %q matches %q", field, normValue, normPattern)
	} else if err != nil {
		// ErrBadPattern:glob 语法错(spec §B 安全降级,不 panic)
		return false, fmt.Sprintf("glob on %q: bad pattern %q: %v", field, pattern, err)
	}

	// 3) basename 兜底:取 normValue 最后一段,应对 `*.go` 匹配 `internal/foo.go`
	//    这样的多段路径场景(spec §B 典型用例)。
	if idx := strings.LastIndex(normValue, "/"); idx >= 0 {
		basename := normValue[idx+1:]
		if ok, err := path.Match(normPattern, basename); err == nil && ok {
			return true, fmt.Sprintf("glob on %q: basename %q matches %q", field, basename, normPattern)
		}
	}

	return false, fmt.Sprintf("glob on %q: %q does not match %q", field, normValue, normPattern)
}

// evaluateContains 实现 contains 操作。
//
// 语义:strings.Contains(ctxField, condValue);ctxField 为空时视为不匹配。
func evaluateContains(field, ctxValue, want string) (bool, string) {
	if ctxValue == "" {
		return false, fmt.Sprintf("contains on %q: context field empty (intentionally non-match)", field)
	}
	if strings.Contains(ctxValue, want) {
		return true, fmt.Sprintf("contains on %q: %q contains %q", field, ctxValue, want)
	}
	return false, fmt.Sprintf("contains on %q: %q does not contain %q", field, ctxValue, want)
}

// normalizePathSeparator 把字符串中所有 `\` 替换为 `/`,用于跨平台 glob 归一化。
//
// [Why 单独函数] tasks.md §3.2 C.13 要求「输入 internal\foo.go 用 glob
// internal/*.go 也要匹配」;直接 strings.ReplaceAll 比 filepath.ToSlash
// 更可控(后者还会做大小写转换等副作用)。
func normalizePathSeparator(s string) string {
	return strings.ReplaceAll(s, `\`, "/")
}

// toComparableString 把 condition Value(任意 JSON 类型)统一转字符串。
//
// 支持:string / bool / 数值 / nil;其它类型(map / slice / 自定义 struct)→
// 空串(spec §B 安全降级,不抛错)。
//
// [Why 不复用 hook.toStringValue] 那个函数在 hook 包内未导出;且其语义是
// 「任意值转字符串」,与本函数「JSON 反序列化值转字符串」语义重叠,
// 重复实现一份以避免 hook → matcher 内部依赖。
func toComparableString(v any) string {
	if v == nil {
		return ""
	}
	switch x := v.(type) {
	case string:
		return x
	case bool:
		if x {
			return "true"
		}
		return "false"
	case float64: // JSON number 默认反序列化为 float64
		// [Why 整型优先] 整数情形(如 {"value": 3})反序列化为 float64,
		// 打印为 "3" 还是 "3.0" 取决于用户预期;这里按 fmt %v 输出,
		// 整数 3 → "3",小数 3.14 → "3.14"。
		return fmt.Sprintf("%v", x)
	case float32:
		return fmt.Sprintf("%v", x)
	case int:
		return fmt.Sprintf("%d", x)
	case int32:
		return fmt.Sprintf("%d", x)
	case int64:
		return fmt.Sprintf("%d", x)
	default:
		// 其它类型(map / slice / struct)→ 视为「非标量值」,spec §B 不支持
		// 跨类型比较,统一返回空串。
		return ""
	}
}

// anyToString 把 HookContext ToolInput 的子值转字符串,容忍 string / 数值 / bool。
//
// [Why 不直接 fmt.Sprint] 数值类型 fmt.Sprint(3) → "3",与 toComparableString
// 行为一致;bool 同理(避免 Sprintf("%t") 带来额外开销)。nil / map / slice /
// 自定义 struct 视为「不可比较」返回 ok=false。
func anyToString(v any) (string, bool) {
	if v == nil {
		return "", false
	}
	switch x := v.(type) {
	case string:
		return x, true
	case bool:
		if x {
			return "true", true
		}
		return "false", true
	case float64:
		return fmt.Sprintf("%v", x), true
	case float32:
		return fmt.Sprintf("%v", x), true
	case int:
		return fmt.Sprintf("%d", x), true
	case int32:
		return fmt.Sprintf("%d", x), true
	case int64:
		return fmt.Sprintf("%d", x), true
	default:
		return "", false
	}
}
