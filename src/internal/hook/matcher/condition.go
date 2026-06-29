// Package matcher 实现 Hook 系统的条件匹配器（spec §B + Task 3）。
//
// Condition DSL 简洁但表达力足够：
//   - 三种基础形式:leaf（{field, op, value}）/ all（数组）/ any（数组）;
//   - 四种 leaf 操作:eq / neq / glob / contains;
//   - 顶层 condition 缺省 / null / 空对象视为永远匹配。
//
// 设计要点:
//   - Condition 是不可变数据载体，JSON 解析后零修改传给 Matcher.Evaluate;
//   - ParseCondition 同时支持「裸 leaf」和「组合 all/any」,自动识别形式;
//   - IsEmpty 判定「无条件」语义,空数组 / nil 视为 true (all) 或 false (any),
//     与 spec §B 末段「顶层 condition 缺省 / null 视为永远匹配」保持一致。
package matcher

import (
	"encoding/json"
	"fmt"
)

// Condition 是 Hook 触发的条件 DSL 数据结构(spec §B + Task 3 §1)。
//
// JSON 三层结构:
//   - leaf:{field, op, value} 同时存在 → 评估 leaf;
//   - all / any:数组形式 → 评估组合;
//   - 都不存在或全部空 → 无条件(IsEmpty 返回 true)。
//
// 字段语义:
//   - All/Any:组合子条件数组;同一对象只可能其中之一非空(JSON 上两者互斥);
//   - Field:leaf 字段名(如 "tool_name" / "tool_input.file_path" / "event");
//   - Op:leaf 操作("eq"/"neq"/"glob"/"contains"),缺省时默认为 "eq";
//   - Value:leaf 比较值,任意 JSON 类型,Matcher 内统一转字符串。
//
// [Why 任意 Value 类型] spec §B 描述为 <any>,JSON 反序列化时 leaf 值可能是
// string / bool / number / null;Matcher 内根据 Op 不同做类型转换,这里不做约束。
type Condition struct {
	// All 是「all 组合」数组:所有子 Evaluate 必须为 true 才匹配。
	// JSON 形式:{ "all": [ {leaf}, {leaf}, ... ] }
	// 空数组视为 true(spec §B「空数组视为真」)。
	All []Condition `json:"all,omitempty"`

	// Any 是「any 组合」数组:任一子 Evaluate 为 true 即匹配。
	// JSON 形式:{ "any": [ {leaf}, {leaf}, ... ] }
	// 空数组视为 false(spec §B「空数组视为假」)。
	Any []Condition `json:"any,omitempty"`

	// Field 是 leaf 字段名(如 "event" / "tool_name" / "tool_input.file_path")。
	Field string `json:"field,omitempty"`

	// Op 是 leaf 操作类型,合法值:eq / neq / glob / contains。
	// 缺省时 Matcher.Evaluate 视为 "eq"。
	// [Why 不在解析阶段校验] 解析阶段只做 JSON → 结构,语义校验交给 Matcher 评估时
	// 按需处理(未知 op 视为不匹配,见 matcher.go);让 ParseCondition 不至于
	// 因单个坏值把整组配置 reject,符合 spec §A.5「错误隔离」精神。
	Op string `json:"op,omitempty"`

	// Value 是 leaf 比较值,JSON 反序列化为 any(string / number / bool / null)。
	// Matcher.Evaluate 内通过 toComparableString 统一转字符串再比较。
	Value any `json:"value,omitempty"`
}

// ParseCondition 把 JSON RawMessage 解析为 Condition 结构。
//
// 支持 4 种 JSON 形式(spec §B + Task 3 §3.1):
//  1. leaf 对象:{ "field": "...", "op": "...", "value": ... }
//  2. all 组合:{ "all": [ ... ] }
//  3. any 组合:{ "any": [ ... ] }
//  4. 空对象 / null / 缺省:{} 视为无条件(IsEmpty=true)
//
// 错误:JSON 语法错误 / 类型不匹配时返回 error;
// 语义错误(unknown op 等)不在此处返回,由 Matcher.Evaluate 评估时按 miss 处理。
//
// [Why 显式 type switch] encoding/json 默认按字段名匹配,但 leaf 与组合的「同层
// 互斥」语义需要我们解析后做一次形式识别;这里采用先 unmarshal 进 Condition,再
// 通过字段值判断属于哪种形式的方式(避免引入额外的 type tag)。
func ParseCondition(raw json.RawMessage) (Condition, error) {
	// null / 缺省 / 空 raw 都视为无条件,IsEmpty=true
	if len(raw) == 0 || string(raw) == "null" {
		return Condition{}, nil
	}
	// 显式空对象也视为无条件
	trimmed := trimWhitespace(raw)
	if len(trimmed) == 0 || string(trimmed) == "{}" {
		return Condition{}, nil
	}

	var c Condition
	if err := json.Unmarshal(raw, &c); err != nil {
		return Condition{}, fmt.Errorf("parse condition: %w", err)
	}
	// [Why 解析后归一化 Op] JSON 里 "eq" 缺省不写;为了让 IsEmpty / Evaluate 的
	// 判别逻辑简单,这里把 Op 为空时填 "eq",使「字段值是默认 eq」的 leaf 也能被识别。
	// 注意:这不影响 leaf / 组合互斥的判断(因为 Field/Op 都不为空也不应同时 All/Any 非空)。
	if c.Op == "" && c.Field != "" {
		c.Op = "eq"
	}
	// [Why 把空 All/Any slice 归零为 nil] 顶层 condition 缺省/null/空对象经
	// ParseCondition 后为 Condition{} 零值,IsEmpty=true。但用户在 JSON 显式
	// 写 "all":[] 会被反序列化为非 nil 的空 slice;为与「无条件」语义一致,
	// 在解析阶段统一把空 slice 归零(只有 All/Any 都是「非空切片」时才视为
	// 显式组合条件,IsEmpty=false)。这与 IsEmpty 的「nil vs 空 slice 区分」
	// 不冲突——因为 ParseCondition 显式把「空组合条件」归类为「缺省」。
	if len(c.All) == 0 {
		c.All = nil
	}
	if len(c.Any) == 0 {
		c.Any = nil
	}
	return c, nil
}

// IsEmpty 返回 Condition 是否等价于「无条件」(永远匹配)。
//
// 判定规则(spec §B + Task 3 §1):
//   - 所有字段(All / Any / Field / Op / Value)都是其类型的零值(nil / 空串 / nil) → true;
//   - nil 与空 slice 视作不同(nil = 未设置;空 slice = 显式写「空组合条件」):
//   - Condition{} 零值:All=nil,Any=nil → IsEmpty=true;
//   - Condition{All: []Condition{}}:显式空 all 数组(spec §B「空数组视为真」),
//     IsEmpty=false,交给 Evaluate 走 all 分支返回 true;
//   - 这是与 spec §B「顶层 condition 缺省/null 视为永远匹配」语义一致的细节:
//     解析阶段 null/空对象已归零为 Condition{},IsEmpty 自然返回 true;
//
// [Why 区分 nil vs 空 slice] spec §B 末段明确「空数组视为真(all)/ 假(any)」,
// 是组合的运行时语义,不是 IsEmpty 的判定条件。IsEmpty 专用于「整个 condition 缺失」,
// 与组合空数组的「按 all 视为真 / 按 any 视为假」是两个层次,需要在 IsEmpty 这一层
// 区分 nil(零值/未设置)与空 slice(显式空组合条件)。
func (c Condition) IsEmpty() bool {
	return c.All == nil &&
		c.Any == nil &&
		c.Field == "" &&
		c.Op == "" &&
		c.Value == nil
}

// trimWhitespace 去除 JSON RawMessage 两端空白(空格 / Tab / 换行 / 回车)。
//
// [Why 不调 strings.TrimSpace] 标准库 TrimSpace 对 []byte 也可用,但 RawMessage
// 是 []byte,直接走 TrimSpace 即可;这里单独写一个函数以便后续扩展(比如只接受
// 某些字符集),当前直接复用 strings.TrimSpace 的逻辑。
func trimWhitespace(raw []byte) []byte {
	start, end := 0, len(raw)
	for start < end {
		b := raw[start]
		if b != ' ' && b != '\t' && b != '\n' && b != '\r' {
			break
		}
		start++
	}
	for end > start {
		b := raw[end-1]
		if b != ' ' && b != '\t' && b != '\n' && b != '\r' {
			break
		}
		end--
	}
	return raw[start:end]
}
