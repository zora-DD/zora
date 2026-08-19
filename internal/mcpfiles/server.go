// Package mcpfiles 提供受目录边界约束的只读文件 MCP Server。
package mcpfiles

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	defaultListLimit = 100
	maxListLimit     = 500
	defaultReadRunes = 12_000
	maxReadRunes     = 50_000
	maxFileBytes     = 2 << 20
)

var errListLimitReached = errors.New("文件列表达到上限")

type listFilesInput struct {
	Path      string `json:"path,omitempty" jsonschema:"相对于授权根目录的子目录，默认为当前根目录"`
	Recursive bool   `json:"recursive,omitempty" jsonschema:"是否递归列出子目录；默认 false"`
	Limit     int    `json:"limit,omitempty" jsonschema:"最多返回多少项，默认 100，最大 500"`
}

type fileEntry struct {
	Path       string `json:"path"`
	IsDir      bool   `json:"is_dir"`
	SizeBytes  int64  `json:"size_bytes"`
	ModifiedAt string `json:"modified_at"`
}

type listFilesOutput struct {
	Directory string      `json:"directory"`
	Entries   []fileEntry `json:"entries"`
	Truncated bool        `json:"truncated"`
}

type readTextFileInput struct {
	Path     string `json:"path" jsonschema:"相对于授权根目录的 UTF-8 文本文件路径"`
	MaxRunes int    `json:"max_runes,omitempty" jsonschema:"最多返回多少个 Unicode 字符，默认 12000，最大 50000"`
}

type readTextFileOutput struct {
	Path      string `json:"path"`
	SizeBytes int64  `json:"size_bytes"`
	Content   string `json:"content"`
	Truncated bool   `json:"truncated"`
}

type connector struct {
	root string
}

// New 创建只读文件 MCP Server。root 必须已存在，且后续所有访问都会再次检查越界和符号链接。
func New(root string) (*mcp.Server, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, fmt.Errorf("文件 MCP Server 必须配置授权根目录")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("解析授权根目录失败：%w", err)
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return nil, fmt.Errorf("读取授权根目录失败：%w", err)
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return nil, fmt.Errorf("读取授权根目录失败：%w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("文件 MCP 授权根路径不是目录：%q", root)
	}

	files := &connector{root: canonical}
	server := mcp.NewServer(&mcp.Implementation{Name: "zora-mcp-files", Version: "0.8.0-dev"}, nil)
	readOnly := true
	openWorld := false
	destructive := false
	annotations := &mcp.ToolAnnotations{
		ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: &openWorld, DestructiveHint: &destructive,
	}
	mcp.AddTool(server, &mcp.Tool{
		Name: "list_files", Title: "列出办公文件", Description: "列出授权目录中的文件和子目录；不跟随符号链接，也不显示隐藏路径。",
		Annotations: annotations,
	}, files.listFiles)
	mcp.AddTool(server, &mcp.Tool{
		Name: "read_text_file", Title: "读取办公文本文件", Description: "读取授权目录中的 UTF-8 文本文件；拒绝隐藏路径、二进制文件和越界路径。",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint: readOnly, IdempotentHint: true, OpenWorldHint: &openWorld, DestructiveHint: &destructive,
		},
	}, files.readTextFile)
	return server, nil
}

func (c *connector) listFiles(_ context.Context, _ *mcp.CallToolRequest, input listFilesInput) (*mcp.CallToolResult, listFilesOutput, error) {
	limit := input.Limit
	if limit == 0 {
		limit = defaultListLimit
	}
	if limit < 1 || limit > maxListLimit {
		return nil, listFilesOutput{}, fmt.Errorf("limit 必须在 1 到 %d 之间", maxListLimit)
	}
	requested := strings.TrimSpace(input.Path)
	if requested == "" {
		requested = "."
	}
	directory, relative, err := c.resolve(requested)
	if err != nil {
		return nil, listFilesOutput{}, err
	}
	info, err := os.Stat(directory)
	if err != nil {
		return nil, listFilesOutput{}, fmt.Errorf("读取目录失败：%w", err)
	}
	if !info.IsDir() {
		return nil, listFilesOutput{}, fmt.Errorf("目标不是目录：%q", requested)
	}

	entries := make([]fileEntry, 0, min(limit, defaultListLimit))
	truncated := false
	appendEntry := func(path string, entry fs.DirEntry) error {
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		relativePath, relErr := filepath.Rel(c.root, path)
		if relErr != nil || !visibleRelativePath(relativePath) {
			return nil
		}
		if len(entries) >= limit {
			truncated = true
			return errListLimitReached
		}
		entryInfo, infoErr := entry.Info()
		if infoErr != nil {
			return infoErr
		}
		entries = append(entries, fileEntry{
			Path: filepath.ToSlash(relativePath), IsDir: entry.IsDir(), SizeBytes: entryInfo.Size(),
			ModifiedAt: entryInfo.ModTime().UTC().Format(time.RFC3339),
		})
		return nil
	}

	if input.Recursive {
		err = filepath.WalkDir(directory, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if path == directory {
				return nil
			}
			if entry.Type()&os.ModeSymlink != 0 {
				if entry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			relativePath, relErr := filepath.Rel(c.root, path)
			if relErr != nil || !visibleRelativePath(relativePath) {
				if entry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			return appendEntry(path, entry)
		})
		if errors.Is(err, errListLimitReached) {
			err = nil
		}
	} else {
		var children []fs.DirEntry
		children, err = os.ReadDir(directory)
		if err == nil {
			for _, entry := range children {
				if appendErr := appendEntry(filepath.Join(directory, entry.Name()), entry); appendErr != nil {
					if !errors.Is(appendErr, errListLimitReached) {
						err = appendErr
					}
					break
				}
			}
		}
	}
	if err != nil {
		return nil, listFilesOutput{}, fmt.Errorf("列出目录失败：%w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return nil, listFilesOutput{Directory: filepath.ToSlash(relative), Entries: entries, Truncated: truncated}, nil
}

func (c *connector) readTextFile(_ context.Context, _ *mcp.CallToolRequest, input readTextFileInput) (*mcp.CallToolResult, readTextFileOutput, error) {
	if strings.TrimSpace(input.Path) == "" {
		return nil, readTextFileOutput{}, fmt.Errorf("path 不能为空")
	}
	maxRunes := input.MaxRunes
	if maxRunes == 0 {
		maxRunes = defaultReadRunes
	}
	if maxRunes < 1 || maxRunes > maxReadRunes {
		return nil, readTextFileOutput{}, fmt.Errorf("max_runes 必须在 1 到 %d 之间", maxReadRunes)
	}
	path, relative, err := c.resolve(input.Path)
	if err != nil {
		return nil, readTextFileOutput{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, readTextFileOutput{}, fmt.Errorf("读取文件信息失败：%w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, readTextFileOutput{}, fmt.Errorf("目标不是普通文件：%q", input.Path)
	}
	if info.Size() > maxFileBytes {
		return nil, readTextFileOutput{}, fmt.Errorf("文件超过 %d MiB 的读取上限", maxFileBytes>>20)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, readTextFileOutput{}, fmt.Errorf("打开文件失败：%w", err)
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, maxFileBytes+1))
	if err != nil {
		return nil, readTextFileOutput{}, fmt.Errorf("读取文件失败：%w", err)
	}
	if !utf8.Valid(content) {
		return nil, readTextFileOutput{}, fmt.Errorf("文件不是合法 UTF-8 文本：%q", input.Path)
	}
	runes := []rune(string(content))
	truncated := len(runes) > maxRunes
	if truncated {
		runes = runes[:maxRunes]
	}
	return nil, readTextFileOutput{
		Path: filepath.ToSlash(relative), SizeBytes: info.Size(), Content: string(runes), Truncated: truncated,
	}, nil
}

func (c *connector) resolve(requested string) (string, string, error) {
	if filepath.IsAbs(requested) {
		return "", "", fmt.Errorf("只允许使用相对于授权根目录的路径")
	}
	cleaned := filepath.Clean(requested)
	if !visibleRelativePath(cleaned) {
		return "", "", fmt.Errorf("不允许访问隐藏路径或授权目录之外的路径：%q", requested)
	}
	candidate := filepath.Join(c.root, cleaned)
	canonical, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", "", fmt.Errorf("目标路径不存在或不可访问：%w", err)
	}
	relative, err := filepath.Rel(c.root, canonical)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("目标路径越过了授权根目录")
	}
	if !visibleRelativePath(relative) {
		return "", "", fmt.Errorf("不允许访问隐藏路径：%q", requested)
	}
	return canonical, relative, nil
}

func visibleRelativePath(path string) bool {
	cleaned := filepath.Clean(path)
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return false
	}
	if cleaned == "." {
		return true
	}
	for _, part := range strings.Split(cleaned, string(filepath.Separator)) {
		if strings.HasPrefix(part, ".") {
			return false
		}
	}
	return true
}
