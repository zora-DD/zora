package mcpfiles

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConnectorListsAndReadsOnlyVisibleTextFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "docs", "计划.md"), []byte("发布日期：2026-09-01\n上线前需要通过回归测试。"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("SECRET=never-expose"), 0o600); err != nil {
		t.Fatal(err)
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	files := &connector{root: canonicalRoot}
	_, listed, err := files.listFiles(context.Background(), nil, listFilesInput{Path: ".", Recursive: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Entries) != 2 || listed.Entries[0].Path != "docs" || listed.Entries[1].Path != "docs/计划.md" {
		t.Fatalf("unexpected entries: %+v", listed.Entries)
	}
	_, limited, err := files.listFiles(context.Background(), nil, listFilesInput{Path: ".", Recursive: true, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(limited.Entries) != 1 || !limited.Truncated {
		t.Fatalf("unexpected limited entries: %+v", limited)
	}
	_, read, err := files.readTextFile(context.Background(), nil, readTextFileInput{Path: "docs/计划.md", MaxRunes: 8})
	if err != nil {
		t.Fatal(err)
	}
	if read.Content != "发布日期：202" || !read.Truncated {
		t.Fatalf("unexpected read result: %+v", read)
	}
	if _, _, err := files.readTextFile(context.Background(), nil, readTextFileInput{Path: ".env"}); err == nil {
		t.Fatal("expected hidden file access to be rejected")
	}
}

func TestConnectorRejectsTraversalAndEscapingSymlink(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	files := &connector{root: canonicalRoot}
	if _, _, err := files.readTextFile(context.Background(), nil, readTextFileInput{Path: "../secret.txt"}); err == nil {
		t.Fatal("expected traversal to be rejected")
	}
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(root, "link.txt")); err != nil {
		t.Skipf("current filesystem does not support symlinks: %v", err)
	}
	if _, _, err := files.readTextFile(context.Background(), nil, readTextFileInput{Path: "link.txt"}); err == nil || !strings.Contains(err.Error(), "授权根目录") {
		t.Fatalf("expected escaping symlink error, got %v", err)
	}
}
