package serve

import (
	"strings"
	"testing"
)

func TestDisplayAddr(t *testing.T) {
	local := localIP()
	cases := []struct {
		in     string
		want   string // wantIP=true 时断言为 "localIP:port" 形式
		wantIP bool
	}{
		{"127.0.0.1:9000", "127.0.0.1:9000", false}, // 显式 host 原样
		{"localhost:9000", "localhost:9000", false}, // 显式 host 原样
		{":37449", local + ":37449", true},          // 空 host 补本机 IP，保留端口
		{"0.0.0.0:8080", local + ":8080", true},     // 通配地址补本机 IP
		{"[::]:8080", local + ":8080", true},        // IPv6 通配替换为 IPv4 本机 IP（无方括号）
		{"no-port", "no-port", false},               // 非法地址原样返回
	}
	for _, c := range cases {
		got := displayAddr(c.in)
		if c.wantIP {
			if !strings.HasSuffix(got, strings.TrimPrefix(c.want, local)) || !strings.HasPrefix(got, local) {
				t.Errorf("displayAddr(%q) = %q, want %q form", c.in, got, c.want)
			}
			continue
		}
		if got != c.want {
			t.Errorf("displayAddr(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
