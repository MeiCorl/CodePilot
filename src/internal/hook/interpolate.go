// Package hook — $VAR_NAME 变量插值的对外别名层(实现见 hookcontext)。
//
// 与 context.go 同理由:executor / matcher / engine 三个子包需要调用
// hook.Interpolate,但 hook 父包不能被 hook/executor 反向 import;
// 把实现放到 hookcontext 子包后,所有调用方通过本文件提供的 var alias
// 透明使用,调用代码 `hook.Interpolate(...)` 无需改写。
package hook

import "github.com/MeiCorl/CodePilot/src/internal/hookcontext"

// Interpolate 把 template 中 $VAR_NAME 占位符替换为 vars 对应值。
// 详细语法见 hookcontext.Interpolate。
var Interpolate = hookcontext.Interpolate
