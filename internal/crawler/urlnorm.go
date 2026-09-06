// Package crawler はクローラ本体。URL の正規化・リンク抽出・重複排除・Worker Pool・取得を担う。
package crawler

import (
	"errors"
	"net/url"
	"strings"
)

// Normalize は絶対 URL を比較可能な形に揃える(SPEC G-01)。
//   - scheme / host を小文字化、既定ポート(http:80 / https:443)を落とす
//   - フラグメントを落とす(同じ資源なので取得は一度でよい)
//   - 空パスは "/" にし、ドット段(./ ../)を解決する
//   - クエリと末尾スラッシュは保つ(別資源になりうる)
//
// 冪等: Normalize(Normalize(u)) == Normalize(u)。
func Normalize(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", err
	}
	if !u.IsAbs() {
		return "", errors.New("not an absolute url: " + raw)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", errors.New("unsupported scheme: " + u.Scheme)
	}
	if u.Hostname() == "" {
		return "", errors.New("missing host: " + raw)
	}
	u.Scheme = scheme
	u.Host = hostKey(u.Host, scheme, false)
	u.Fragment = ""
	u.RawFragment = ""
	if u.Path == "" {
		u.Path = "/"
		u.RawPath = ""
	}
	// ドット段の除去: ResolveReference は ref の path に対して RFC 3986 §5.2.4 の除去を行う
	ref := &url.URL{Path: u.Path, RawPath: u.RawPath, RawQuery: u.RawQuery, ForceQuery: u.ForceQuery}
	resolved := u.ResolveReference(ref)
	return resolved.String(), nil
}

// hostKey は host(port 付きかもしれない)を小文字化し、scheme の既定ポートを落とす。
// stripWWW が真なら先頭の "www." も落とす(同一ドメイン判定用・§2.3)。
func hostKey(hostport, scheme string, stripWWW bool) string {
	h := strings.ToLower(hostport)
	host, port := h, ""
	if i := strings.LastIndex(h, ":"); i >= 0 && !strings.HasSuffix(h, "]") && !strings.Contains(h[i:], "]") {
		host, port = h[:i], h[i+1:]
	}
	if (scheme == "http" && port == "80") || (scheme == "https" && port == "443") {
		port = ""
	}
	if stripWWW {
		host = strings.TrimPrefix(host, "www.")
	}
	if port != "" {
		return host + ":" + port
	}
	return host
}

// SameDomain は link が start と同一ドメイン(§2.3)かを返す。
// ホストの完全一致、または "www." の有無だけの差を同一と見なす。ポート差は別。scheme 差は同一。
func SameDomain(start, link string) bool {
	a, err1 := url.Parse(start)
	b, err2 := url.Parse(link)
	if err1 != nil || err2 != nil || a.Host == "" || b.Host == "" {
		return false
	}
	return hostKey(a.Host, strings.ToLower(a.Scheme), true) == hostKey(b.Host, strings.ToLower(b.Scheme), true)
}
