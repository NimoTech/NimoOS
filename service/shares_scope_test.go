package service

import "testing"

// sharePathScope 决定「删除目录时清理哪些分享」的 SQL 匹配范围——边界必须
// 精确:删 /a/b 要覆盖 /a/b 自身与 /a/b/c,绝不能波及 /a/bc。
func TestSharePathScope(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		exact   string
		subtree string
	}{
		{"普通路径", "/a/b", "/a/b", "/a/b/%"},
		{"尾斜杠归一", "/a/b/", "/a/b", "/a/b/%"},
		{"LIKE 通配符转义", "/a/100%_done", "/a/100%_done", `/a/100\%\_done/%`},
		{"根路径", "/", "/", "/%"},
		{"空串按根处理", "", "/", "/%"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			exact, subtree := sharePathScope(c.in)
			if exact != c.exact || subtree != c.subtree {
				t.Fatalf("sharePathScope(%q) = (%q, %q), want (%q, %q)", c.in, exact, subtree, c.exact, c.subtree)
			}
		})
	}
}

// 语义自证:子树模式 + "/" 边界的组合下,/a/bc 不应命中 /a/b 的清理范围。
// (用 Go 侧等价前缀判断模拟 LIKE 语义;真实 SQL 由 DeleteShareByPath 拼接,
// 模式串本身已在上面的用例中锁定。)
func TestSharePathScopeBoundary(t *testing.T) {
	exact, _ := sharePathScope("/a/b")
	for path, want := range map[string]bool{
		"/a/b":    true,  // 分享就挂在被删目录上
		"/a/b/c":  true,  // 子树内
		"/a/bc":   false, // 同前缀兄弟目录,绝不能删
		"/a":      false, // 祖先
		"/a/b2/c": false,
	} {
		got := path == exact || (len(path) > len(exact)+1 && path[:len(exact)+1] == exact+"/")
		if got != want {
			t.Fatalf("path %q in scope of /a/b = %v, want %v", path, got, want)
		}
	}
}
