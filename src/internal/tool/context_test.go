package tool

import (
	"context"
	"testing"
)

func TestWithToolUseID_RoundTrip(t *testing.T) {
	ctx := WithToolUseID(context.Background(), "tool-123")
	id, ok := ToolUseIDFromContext(ctx)
	if !ok || id != "tool-123" {
		t.Fatalf("期望拿到 tool-123, 实际 ok=%v id=%q", ok, id)
	}
}

func TestWithToolUseID_EmptyIDNotInjected(t *testing.T) {
	ctx := WithToolUseID(context.Background(), "")
	if _, ok := ToolUseIDFromContext(ctx); ok {
		t.Fatalf("空 id 不应被注入, 应返回 ok=false")
	}
}

func TestToolUseIDFromContext_NilCtx(t *testing.T) {
	if _, ok := ToolUseIDFromContext(nil); ok {
		t.Fatalf("nil ctx 不应拿到 id")
	}
}

func TestToolUseIDFromContext_NoValue(t *testing.T) {
	if _, ok := ToolUseIDFromContext(context.Background()); ok {
		t.Fatalf("未注入的 ctx 不应拿到 id")
	}
}
