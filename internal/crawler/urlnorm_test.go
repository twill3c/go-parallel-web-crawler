package crawler

import "testing"

// T-104 / G-01: 正規化の表。期待値の出所: SPEC G-01(フラグメント除去・ホスト小文字化・
// 既定ポート除去・空パス→"/")と RFC 3986 §5.2.4(ドット段の除去)。
func TestNormalize_Table(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://example.com", "https://example.com/"},
		{"https://Example.COM/", "https://example.com/"},
		{"HTTPS://example.com/", "https://example.com/"},
		{"https://example.com:443/a", "https://example.com/a"},
		{"http://example.com:80/a", "http://example.com/a"},
		{"http://example.com:8080/a", "http://example.com:8080/a"},
		{"https://example.com/a#section", "https://example.com/a"},
		{"https://example.com/a?x=1&y=2#f", "https://example.com/a?x=1&y=2"},
		{"https://example.com/a/b/../c", "https://example.com/a/c"},
		{"https://example.com/a/./b", "https://example.com/a/b"},
		{"https://example.com/a/", "https://example.com/a/"}, // 末尾スラッシュは保つ(別資源になりうる)
		{"https://example.com/a%20b", "https://example.com/a%20b"},
		{"https://example.com/日本", "https://example.com/%E6%97%A5%E6%9C%AC"},
	}
	for _, c := range cases {
		got, err := Normalize(c.in)
		if err != nil {
			t.Fatalf("%q: %v", c.in, err)
		}
		if got != c.want {
			t.Errorf("Normalize(%q) = %q, want %q", c.in, got, c.want)
		}
		// 冪等性(G-01)
		again, err := Normalize(got)
		if err != nil || again != got {
			t.Errorf("not idempotent: %q -> %q -> %q (%v)", c.in, got, again, err)
		}
	}
}

func TestNormalize_Rejects(t *testing.T) {
	for _, in := range []string{"", "not a url", "mailto:a@example.com", "javascript:void(0)", "//example.com/x", "/relative"} {
		if got, err := Normalize(in); err == nil {
			t.Errorf("Normalize(%q) = %q, want error", in, got)
		}
	}
}

// T-105 / F-06: 同一ドメインの表。期待値の出所: SPEC §2.3。
func TestSameDomain_Table(t *testing.T) {
	cases := []struct {
		start, link string
		want        bool
	}{
		{"https://example.com/", "https://example.com/about", true},
		{"https://example.com/", "https://EXAMPLE.com/about", true},
		{"https://example.com/", "https://www.example.com/about", true},
		{"https://www.example.com/", "https://example.com/about", true},
		{"https://example.com/", "http://example.com/about", true},        // scheme 違いは同一ホスト
		{"https://example.com/", "https://example.com:443/about", true},   // 既定ポート
		{"https://example.com/", "https://example.com:8443/about", false}, // ポート差は別
		{"https://example.com/", "https://blog.example.com/", false},      // サブドメイン
		{"https://example.com/", "https://example.org/", false},
		{"https://example.com/", "https://notexample.com/", false},
		{"https://example.com/", "https://www.example.com.evil.test/", false},
	}
	for _, c := range cases {
		if got := SameDomain(c.start, c.link); got != c.want {
			t.Errorf("SameDomain(%q, %q) = %v, want %v", c.start, c.link, got, c.want)
		}
	}
}
