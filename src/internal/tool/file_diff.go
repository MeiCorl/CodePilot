package tool

// FileDiffEntry 是工具侧往 DiffSink 写入时的最小数据单元。
//
// 字段保持最小集（路径 + 改动前后内容），不携带 UpdatedAt / Language 等
// 接收方专属元数据，由接收方按需填充。Language 在 web 侧按后缀推导，
// UpdatedAt 在 web 侧按写入时间填充。
//
// 该类型定义在 tool 包（最底座），同时被 builtin 工具与 web 侧 Store 引用，
// 避免 web → builtin 的反向依赖。
type FileDiffEntry struct {
	FilePath string
	Before   string
	After    string
}

// FileDiffSink 是工具侧依赖的"文件 diff 接收器"抽象。
//
// 任何实现了 Set(toolUseID, entry) bool 的类型都可作为 Sink 注入；
// 返回 false 表示拒绝写入（容量超限 / id 为空等），工具侧应忽略。
//
// 设计动机：让 web.FileDiffStore 与内置工具之间的耦合走"消费者侧定义
// interface"模式，工具仅依赖该抽象，不反向依赖 web 包。
type FileDiffSink interface {
	Set(toolUseID string, entry FileDiffEntry) bool
}
