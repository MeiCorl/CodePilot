// Package reconnect 提供 MCP Session 断开后的指数退避重试策略。
//
// 设计目标：
//   - 退避节奏与具体传输层（stdio / http）解耦，由 Backoff 统一表达
//   - 退避计算无副作用（纯函数），便于单元测试
//   - 默认 1s/3s/9s 三次重试后由调用方决定是否标记永久 unhealthy
//
// 与 Session 的协作：
//   - Session.EnsureHealthy() 持有 Backoff 引用
//   - 每次重连失败后调用 NextDelay(attempt) 获取下次等待时长
//   - Backoff 本身不睡眠：是否 sleep 由 Session.EnsureHealthy 用 time.After 实现
//
// 为什么不直接用 golang.org/x/time/rate 或 backoff 库：
//   - 本包仅 30 余行，引入第三方依赖得不偿失
//   - 退避节奏与 MCP 业务语义绑定（默认 1s/3s/9s），不追求通用性
package reconnect

import (
	"errors"
	"time"
)

// ErrExhausted 在重试次数耗尽后返回，调用方应将 Session 标记为永久 unhealthy。
//
// 与 errors.Is 配合使用：调用方在 EnsureHealthy 拿到本错误后应放弃 lazy
// 重连，把决策权交给用户（通常是「重启 CodePilot 恢复」）。
var ErrExhausted = errors.New("reconnect: 重试次数已耗尽")

// Backoff 描述一组退避节奏与最大尝试次数。
//
// 不可变：构造后 intervals / maxAttempts 不变；调用方并发调用 NextDelay 安全。
type Backoff struct {
	// intervals 索引 attempt（从 0 开始）对应的等待时长。
	// 默认 [1s, 3s, 9s]，共 3 次尝试。
	intervals []time.Duration
}

// NewDefaultBackoff 返回 spec 约定的 1s/3s/9s 退避器（共 4 次 attempts）。
//
// 与 docs/step6-MCP协议实现/spec.md「指数退避重连」段一致：stdio 子进程崩溃
// 或 HTTP 连接断开后按 1s/3s/9s 退避重试 3 次，超过后该 server 标记 unhealthy。
//
// 实现细节：intervals 长度为 4 = 3 次退避 + 1 次「不 sleep 的最终确认」：
//   - attempt 0 失败 → sleep 1s
//   - attempt 1 失败 → sleep 3s
//   - attempt 2 失败 → sleep 9s
//   - attempt 3（第 4 次）失败 → 立即标记 unhealthy（不 sleep）
//
// MaxAttempts=4 让「第 3 次退避后第 4 次成功」成为可能（例如 3 次全失败但
// 第 4 次恢复的场景），同时保留 spec 描述的「3 次后 unhealthy」语义。
func NewDefaultBackoff() *Backoff {
	return &Backoff{
		intervals: []time.Duration{
			1 * time.Second,
			3 * time.Second,
			9 * time.Second,
			0, // 第 4 次不 sleep（最终确认）
		},
	}
}

// NewBackoff 用自定义节奏构造退避器。
//
// intervals 为空时回退到 NewDefaultBackoff；拷贝入参避免外部修改影响内部状态。
// 常用于单元测试（缩短等待时长）或配置驱动的退避（未来从 setting.json 读取）。
func NewBackoff(intervals []time.Duration) *Backoff {
	if len(intervals) == 0 {
		return NewDefaultBackoff()
	}
	cp := make([]time.Duration, len(intervals))
	copy(cp, intervals)
	return &Backoff{intervals: cp}
}

// MaxAttempts 返回总尝试次数（即 intervals 长度）。
//
// 调用方可用此值作为重试上限的判断依据，避免硬编码数字。
func (b *Backoff) MaxAttempts() int {
	return len(b.intervals)
}

// Intervals 返回退避节奏的副本（只读）。
//
// 用于诊断日志展示与单元测试断言。
func (b *Backoff) Intervals() []time.Duration {
	out := make([]time.Duration, len(b.intervals))
	copy(out, b.intervals)
	return out
}

// NextDelay 返回第 attempt 次重试（从 0 开始）应等待的时长。
//
// 返回值：
//   - (delay, true)  ：第 attempt 次是有效的，应等待 delay 后重试
//   - (0, false)     ：attempt 越界（< 0 或 >= MaxAttempts），重试已耗尽
//
// 调用方应配合 attempt++ 与 maxAttempts 自检实现循环：
//
//	for attempt := 0; ; attempt++ {
//	    delay, ok := b.NextDelay(attempt)
//	    if !ok { return ErrExhausted }
//	    select {
//	    case <-time.After(delay):
//	    case <-ctx.Done():
//	        return ctx.Err()
//	    }
//	    if err := tryReconnect(); err == nil { return nil }
//	}
func (b *Backoff) NextDelay(attempt int) (time.Duration, bool) {
	if attempt < 0 || attempt >= len(b.intervals) {
		return 0, false
	}
	return b.intervals[attempt], true
}
