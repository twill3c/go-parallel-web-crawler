package security

import (
	"context"
	"net"
	"strings"
	"testing"
)

// fixedResolver はテスト用の名前解決。ネットワークに出ない(N-02)。
func fixedResolver(table map[string][]string) Resolver {
	return func(_ context.Context, host string) ([]net.IP, error) {
		addrs, ok := table[host]
		if !ok {
			return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
		}
		var ips []net.IP
		for _, a := range addrs {
			ips = append(ips, net.ParseIP(a))
		}
		return ips, nil
	}
}

// T-101 / F-02, G-02: SSRF 拒否表(SPEC §2.4)の各行を拒否する。理由は行ごとに異なる。
// 期待値の出所: SPEC §2.4。アドレス範囲は RFC 1918 / RFC 4193 / RFC 3927 / RFC 6598 /
// RFC 4291(::1, ::, fe80::/10, ff00::/8)/ AWS 等のメタデータ IP 169.254.169.254
func TestValidateURL_RejectsInternal(t *testing.T) {
	v := &Validator{Resolve: fixedResolver(map[string][]string{
		"internal.example.com": {"10.0.0.5"},
		"corp.example.com":     {"172.16.4.4"},
		"home.example.com":     {"192.168.1.1"},
		"cgnat.example.com":    {"100.64.0.1"},
		"ula.example.com":      {"fd12::1"},
		"ll6.example.com":      {"fe80::1"},
		"v4mapped.example.com": {"::ffff:127.0.0.1"},
	})}
	cases := []struct {
		name, raw, reason string
	}{
		{"localhost", "http://localhost/", "hostname"},
		{"sub.localhost", "http://api.localhost/", "hostname"},
		{"dot-local", "http://printer.local/", "hostname"},
		{"dot-internal", "http://db.internal/", "hostname"},
		{"loopback v4", "http://127.0.0.1/", "loopback"},
		{"loopback v4 other", "http://127.8.8.8/", "loopback"},
		{"loopback v6", "http://[::1]/", "loopback"},
		{"private 10/8", "http://10.1.2.3/", "private"},
		{"private 172.16/12", "http://172.31.255.1/", "private"},
		{"private 192.168/16", "http://192.168.0.10/", "private"},
		{"private ula", "http://[fc00::1]/", "private"},
		{"link-local v4", "http://169.254.1.1/", "link-local"},
		{"metadata", "http://169.254.169.254/latest/meta-data/", "link-local"},
		{"link-local v6", "http://[fe80::1]/", "link-local"},
		{"unspecified v4", "http://0.0.0.0/", "unspecified"},
		{"unspecified v6", "http://[::]/", "unspecified"},
		{"multicast", "http://224.0.0.1/", "multicast"},
		{"cgnat", "http://100.64.0.1/", "shared"},
		{"scheme ftp", "ftp://example.com/", "scheme"},
		{"scheme file", "file:///etc/passwd", "scheme"},
		{"scheme missing", "example.com/", "scheme"},
		{"userinfo", "http://user:pass@example.com/", "userinfo"},
		{"no host", "http:///path", "host"},
		{"resolves private", "http://internal.example.com/", "private"},
		{"resolves 172", "http://corp.example.com/", "private"},
		{"resolves 192", "http://home.example.com/", "private"},
		{"resolves cgnat", "http://cgnat.example.com/", "shared"},
		{"resolves ula", "http://ula.example.com/", "private"},
		{"resolves ll6", "http://ll6.example.com/", "link-local"},
		{"resolves v4-mapped loopback", "http://v4mapped.example.com/", "loopback"},
		{"unresolvable", "http://nope.example.com/", "resolve"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := v.ValidateURL(context.Background(), c.raw)
			if err == nil {
				t.Fatalf("%q: 通ってしまった(拒否されるべき)", c.raw)
			}
			if !strings.Contains(err.Error(), c.reason) {
				t.Fatalf("%q: reason = %q, want to contain %q", c.raw, err.Error(), c.reason)
			}
		})
	}
}

// T-102 / G-02 陰性対照: 公開アドレス相当は通す。
// 93.184.216.34 / 2606:2800:220:1:248:1893:25c8:1946 は example.com が 2024 年まで使っていた公開アドレス
// (出典: IANA の example.com 予約と過去の DNS 応答)。ここでは固定解決なので値の現在性は問わない。
func TestValidateURL_AllowsPublic(t *testing.T) {
	v := &Validator{Resolve: fixedResolver(map[string][]string{
		"example.com":     {"93.184.216.34", "2606:2800:220:1:248:1893:25c8:1946"},
		"www.example.com": {"93.184.216.34"},
	})}
	cases := []string{
		"https://example.com",
		"http://example.com/path?q=1#frag",
		"https://www.example.com:8443/",
		"http://93.184.216.34/",
		"http://[2606:2800:220:1:248:1893:25c8:1946]/",
		"http://8.8.8.8/",
		"https://Example.COM/Upper",
	}
	for _, raw := range cases {
		u, err := v.ValidateURL(context.Background(), raw)
		if err != nil {
			t.Fatalf("%q: 拒否された: %v", raw, err)
		}
		if u == nil || u.Host == "" {
			t.Fatalf("%q: URL が返らない", raw)
		}
	}
}

// T-103 / G-02 陽性対照: 名前解決が公開 IP と私有 IP を混ぜて返すとき、一つでも該当すれば拒否。
func TestValidateURL_MixedResolutionIsRejected(t *testing.T) {
	v := &Validator{Resolve: fixedResolver(map[string][]string{
		"mixed.example.com": {"93.184.216.34", "10.0.0.1"},
	})}
	// 前提の検算: この解決表が実際に公開と私有を混ぜていること(HC-070)
	ips, _ := v.Resolve(context.Background(), "mixed.example.com")
	var pub, priv int
	for _, ip := range ips {
		if ok, _ := IsForbiddenIP(ip); ok {
			priv++
		} else {
			pub++
		}
	}
	if pub == 0 || priv == 0 {
		t.Fatalf("フィクスチャが混在していない: public=%d private=%d", pub, priv)
	}
	if _, err := v.ValidateURL(context.Background(), "http://mixed.example.com/"); err == nil {
		t.Fatal("混在解決が通ってしまった")
	}
}

// T-101 補: IsForbiddenIP の理由文字列は §2.4 の行と一対一で、公開 IP は空。
func TestIsForbiddenIP_Table(t *testing.T) {
	cases := map[string]string{
		"127.0.0.1":       "loopback",
		"::1":             "loopback",
		"10.0.0.1":        "private",
		"172.16.0.1":      "private",
		"172.32.0.1":      "", // 172.16/12 の外
		"192.168.1.1":     "private",
		"192.169.0.1":     "", // 192.168/16 の外
		"fd00::1":         "private",
		"169.254.169.254": "link-local",
		"fe80::1":         "link-local",
		"0.0.0.0":         "unspecified",
		"::":              "unspecified",
		"224.0.0.1":       "multicast",
		"ff02::1":         "multicast",
		"100.64.0.1":      "shared",
		"100.128.0.1":     "", // 100.64/10 の外
		"8.8.8.8":         "",
		"2001:4860::8888": "",
	}
	for s, want := range cases {
		ip := net.ParseIP(s)
		if ip == nil {
			t.Fatalf("フィクスチャの IP が不正: %q", s)
		}
		got, reason := IsForbiddenIP(ip)
		if got != (want != "") || reason != want {
			t.Errorf("%s: (%v,%q), want %q", s, got, reason, want)
		}
	}
}

// DialControl は接続直前に相手 IP を再検査する(DNS リバインディング対策・§2.4)。
func TestDialControl(t *testing.T) {
	if err := DialControl("tcp4", "10.0.0.1:80"); err == nil {
		t.Fatal("私有 IP への dial が通ってしまった")
	}
	if err := DialControl("tcp4", "93.184.216.34:443"); err != nil {
		t.Fatalf("公開 IP への dial が拒否された: %v", err)
	}
	if err := DialControl("tcp6", "[fe80::1]:80"); err == nil {
		t.Fatal("リンクローカル v6 への dial が通ってしまった")
	}
}
