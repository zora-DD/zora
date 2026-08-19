// Package secrets 从环境变量或只读文件加载敏感配置，避免密钥直接出现在环境模板和启动参数中。
package secrets

import (
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
)

const maxSecretBytes = 64 << 10

// Resolve 优先拒绝歧义配置：valueEnv 与 fileEnv 不能同时设置。
// 文件必须是普通文件，并在 Unix 上禁止 group/other 权限，防止误挂载为公开 Secret。
func Resolve(valueEnv, fileEnv string) (string, error) {
	value := strings.TrimSpace(os.Getenv(valueEnv))
	path := strings.TrimSpace(os.Getenv(fileEnv))
	if value != "" && path != "" {
		return "", fmt.Errorf("%s 与 %s 不能同时配置", valueEnv, fileEnv)
	}
	if path == "" {
		return value, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("读取 %s 指向的 Secret 文件失败：%w", fileEnv, err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", fmt.Errorf("检查 %s 指向的 Secret 文件失败：%w", fileEnv, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s 必须指向普通文件", fileEnv)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("%s 的权限过宽（%04o），请使用 0400 或 0600", fileEnv, info.Mode().Perm())
	}
	if info.Size() > maxSecretBytes {
		return "", fmt.Errorf("%s 指向的 Secret 文件不能超过 64 KiB", fileEnv)
	}
	contents, err := io.ReadAll(io.LimitReader(file, maxSecretBytes+1))
	if err != nil {
		return "", fmt.Errorf("读取 %s 指向的 Secret 文件失败：%w", fileEnv, err)
	}
	if len(contents) > maxSecretBytes {
		return "", fmt.Errorf("%s 指向的 Secret 文件不能超过 64 KiB", fileEnv)
	}
	secret := strings.TrimSpace(string(contents))
	if secret == "" {
		return "", fmt.Errorf("%s 指向的 Secret 文件不能为空", fileEnv)
	}
	return secret, nil
}

// ResolveEnvironment 支持模型 api_key_env 的同名 _FILE 约定。
func ResolveEnvironment(name string) (string, error) {
	return Resolve(name, name+"_FILE")
}
