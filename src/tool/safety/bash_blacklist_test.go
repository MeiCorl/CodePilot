package safety

import (
	"errors"
	"testing"
)

// TestCheckBashCommandDangerousCases 验证所有已知危险命令被拦截。
func TestCheckBashCommandDangerousCases(t *testing.T) {
	cases := []struct {
		name string
		cmd  string
	}{
		{"rm_rf_root", "rm -rf /"},
		{"rm_rf_root_wildcard", "rm -rf /*"},
		{"rm_rf_home", "rm -rf ~"},
		{"rm_rf_home_dollar", "rm -rf $HOME"},
		{"rm_rf_dash", "rm -r -f /"},
		{"mkfs", "mkfs.ext4 /dev/sda1"},
		{"mkfs_no_suffix", "mkfs /dev/sda1"},
		{"shutdown", "shutdown -h now"},
		{"reboot", "reboot"},
		{"halt", "halt"},
		{"init_0", "init 0"},
		{"dd_to_dev", "dd if=/dev/zero of=/dev/sda"},
		{"redirect_to_dev", "echo x > /dev/sda"},
		{"fork_bomb", ":(){:|:&};:"},
		{"chmod_777_root", "chmod 777 /"},
		{"chmod_777_root_dashR", "chmod -R 777 /etc"},
		{"empty_command", ""},
		{"whitespace_only", "   \t  "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckBashCommand(tc.cmd)
			if err == nil {
				t.Fatalf("应被拦截, 实际放行: %q", tc.cmd)
			}
			if !errors.Is(err, ErrDangerousCommand) {
				t.Errorf("错误类型错误: %v", err)
			}
		})
	}
}

// TestCheckBashCommandSafeCases 验证正常命令放行。
func TestCheckBashCommandSafeCases(t *testing.T) {
	safe := []string{
		"ls",
		"ls -la",
		"cat /tmp/test.txt",
		"go build ./...",
		"go test ./src/tool/...",
		"echo hello",
		"git status",
		"git log --oneline -10",
		"rm tmp.log",                 // 删当前目录的单个文件，非危险
		"mkdir -p /tmp/codepilot/x",  // /tmp 下创建目录
		"grep -r TODO src/",
		"find . -name '*.go'",
		"python3 script.py",
	}
	for _, cmd := range safe {
		t.Run(cmd, func(t *testing.T) {
			if err := CheckBashCommand(cmd); err != nil {
				t.Errorf("正常命令应放行, 实际被拦截: %q (err=%v)", cmd, err)
			}
		})
	}
}

// TestDangerousCommandErrorMessage 验证错误消息包含可读原因。
func TestDangerousCommandErrorMessage(t *testing.T) {
	err := CheckBashCommand("rm -rf /")
	if err == nil {
		t.Fatal("应被拦截")
	}
	if !contains(err.Error(), "根目录") && !contains(err.Error(), "家目录") {
		t.Errorf("错误消息应说明具体拦截原因, 实际: %s", err.Error())
	}
}

// contains 简单子串检查，测试中避免引入 strings。
func contains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
