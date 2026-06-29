package integration

import "github.com/MeiCorl/CodePilot/src/internal/hook"

// RegisterPromptSink 返回传入的 PromptSink。该函数让 main.go 的装配点显式表达 prompt action 注入链路。
func RegisterPromptSink(sink hook.PromptSink) hook.PromptSink {
	return sink
}
