// 本地 echo server：用于接收 CodePilot http hook 的 POST 请求
// 把所有请求写到本地日志文件供 e2e 验证使用。
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

func main() {
	port := os.Getenv("HOOK_TEST_PORT")
	if port == "" {
		port = "8765"
	}
	logPath := os.Getenv("HOOK_TEST_LOG")
	if logPath == "" {
		logPath = filepath.Join(os.TempDir(), "codepilot-hook-test.log")
	}
	addr := "127.0.0.1:" + port

	// 启动时清空日志
	_ = os.WriteFile(logPath, []byte("=== hook test echo server start @ "+time.Now().Format(time.RFC3339)+" ===\n"), 0644)

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		var body []byte
		if r.Body != nil {
			body, _ = io.ReadAll(r.Body)
		}
		entry := map[string]any{
			"ts":      time.Now().Format(time.RFC3339Nano),
			"method":  r.Method,
			"path":    r.URL.Path,
			"headers": r.Header,
			"body":    string(body),
		}
		b, _ := json.Marshal(entry)
		line := string(b) + "\n"
		f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err == nil {
			_, _ = f.WriteString(line)
			_ = f.Close()
		}
		log.Printf("[%s] %s %s -> body=%d bytes", entry["ts"], r.Method, r.URL.Path, len(body))
		w.WriteHeader(http.StatusAccepted)
		_, _ = fmt.Fprintf(w, "ok")
	})

	log.Printf("hook-test echo server listening on %s, log=%s", addr, logPath)
	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Fatalf("server: %v", err)
	}
}