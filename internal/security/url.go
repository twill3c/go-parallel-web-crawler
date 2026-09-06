// Package security は外部から受け取った URL の検証(SSRF 対策・SPEC §2.4)を担う。
//
// 二段で守る:
//  1. ValidateURL — 受け取った時点で scheme / host / userinfo / ホスト名 / 名前解決した全 IP を検査する
//  2. DialControl — 実際に接続する直前に相手 IP をもう一度検査する(DNS リバインディング対策)
package security

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// Resolver はホスト名を IP の列に解く関数。テストでは固定表に差し替える(N-02)。
type Resolver func(ctx context.Context, host string) ([]net.IP, error)

// Validator は URL 検証器。Resolve が nil なら net.DefaultResolver を使う。
type Validator struct {
	Resolve Resolver
}

// DefaultResolver は OS の名前解決を使う Resolver。
func DefaultResolver(ctx context.Context, host string) ([]net.IP, error) {
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	ips := make([]net.IP, 0, len(addrs))
	for _, a := range addrs {
		ips = append(ips, a.IP)
	}
	return ips, nil
}

// ErrForbidden は検証で拒否された URL に対する基底エラー。
var ErrForbidden = errors.New("forbidden url")

func reject(reason string, detail string) error {
	return fmt.Errorf("%w: %s (%s)", ErrForbidden, reason, detail)
}

// ValidateURL は raw を解析し、§2.4 の拒否表に当たれば error を返す。通れば解析済み URL を返す。
// 名前解決は Validator.Resolve で行い、返った**全アドレス**を検査する(一つでも該当すれば拒否)。
func (v *Validator) ValidateURL(ctx context.Context, raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, reject("invalid url", err.Error())
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return nil, reject("scheme must be http or https", "scheme="+u.Scheme)
	}
	if u.User != nil {
		return nil, reject("userinfo is not allowed", "url contains credentials")
	}
	host := strings.ToLower(u.Hostname()) // DNS は大文字小文字を区別しない。解決表も小文字で引く
	if host == "" {
		return nil, reject("host is missing", raw)
	}
	if bad, reason := IsForbiddenHost(host); bad {
		return nil, reject("hostname is "+reason, host)
	}
	if ip := net.ParseIP(host); ip != nil {
		if bad, reason := IsForbiddenIP(ip); bad {
			return nil, reject("address is "+reason, ip.String())
		}
		return u, nil
	}
	resolve := v.Resolve
	if resolve == nil {
		resolve = DefaultResolver
	}
	ips, err := resolve(ctx, host)
	if err != nil {
		return nil, reject("could not resolve host", err.Error())
	}
	if len(ips) == 0 {
		return nil, reject("could not resolve host", "no addresses")
	}
	for _, ip := range ips {
		if bad, reason := IsForbiddenIP(ip); bad {
			return nil, reject("address is "+reason, host+" -> "+ip.String())
		}
	}
	return u, nil
}

// IsForbiddenHost はホスト名だけで拒否できるもの(名前解決に頼らない)。
func IsForbiddenHost(host string) (bool, string) {
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return true, "localhost hostname"
	}
	if strings.HasSuffix(h, ".local") || strings.HasSuffix(h, ".internal") {
		return true, "internal hostname"
	}
	return false, ""
}

// 100.64.0.0/10(RFC 6598・共有アドレス空間 = CGNAT)。net.IP には判定メソッドが無いので自前で持つ。
var sharedV4 = mustCIDR("100.64.0.0/10")

func mustCIDR(s string) *net.IPNet {
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		panic(err)
	}
	return n
}

// IsForbiddenIP は §2.4 の表に当たる IP なら (true, 理由) を返す。
// 理由文字列は表の行と一対一: loopback / private / link-local / unspecified / multicast / shared。
// IPv4 射影アドレス(::ffff:a.b.c.d)は net.IP の各判定が IPv4 として扱うので、そのまま通る。
func IsForbiddenIP(ip net.IP) (bool, string) {
	switch {
	case ip.IsUnspecified():
		return true, "unspecified"
	case ip.IsLoopback():
		return true, "loopback"
	case ip.IsMulticast() || ip.IsInterfaceLocalMulticast():
		return true, "multicast"
	case ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast():
		return true, "link-local"
	case ip.IsPrivate():
		return true, "private"
	case ip.To4() != nil && sharedV4.Contains(ip.To4()):
		return true, "shared"
	}
	return false, ""
}

// DialControl は net.Dialer.Control に渡す関数。接続先アドレス(host:port、host は IP)を再検査する。
// ValidateURL が通した名前でも、接続時に別の IP へ解決されうる(DNS リバインディング)。
func DialControl(network, address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return reject("bad dial address", address)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return reject("dial address is not an ip", host)
	}
	if bad, reason := IsForbiddenIP(ip); bad {
		return reject("dial target is "+reason, ip.String())
	}
	return nil
}
