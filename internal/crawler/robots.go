package crawler

import (
	"bufio"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// robots.txt(RFC 9309)の解析と判定。
//
// 実装したのは RFC の中核だけである:
//   - 群の選択(大文字小文字を無視・複数一致は結合・一致が無ければ `*`)
//   - Allow / Disallow の最長一致、同長なら Allow
//   - パターンの `*`(0 個以上の任意文字)と `$`(末尾)
//   - Crawl-delay(RFC の規範ではないが広く使われる。Request Delay の下限にする)
//   - Sitemap 行の収集(表示のみ。SPEC のスコープ外なので辿らない)
//
// 実装していないもの: robots.txt 自体のリダイレクト追跡は http.Client 任せ(同一ドメイン制限が
// そのまま効く)、キャッシュ(1 クロール 1 回しか読まないので不要)。

// RobotsMaxBytes は robots.txt の読取上限。RFC 9309 は「解析上限は少なくとも 500 KiB」と定める。
const RobotsMaxBytes = 512 * 1024

// UserAgentToken は robots.txt の群を選ぶときに名乗る名前(product token)。
const UserAgentToken = "GoParallelWebCrawler"

// robotsRule は 1 本の Allow / Disallow。
type robotsRule struct {
	pattern string // 元のパターン(復号済み)
	allow   bool
	length  int // 一致の強さ。RFC 9309 は「オクテット数が多い方が具体的」と定める
}

// Robots は 1 ホスト分の判定器。ParseRobots が組み立てる。
type Robots struct {
	rules      []robotsRule
	CrawlDelay time.Duration
	Sitemaps   []string
	// Groups は自分に一致した群の数(0 なら `*` も含めて一致が無く、規則は適用されない)
	Groups int
}

// ParseRobots は robots.txt を読み、agent に適用される群だけを取り出した判定器を返す。
// 読取は RobotsMaxBytes で打ち切る。壊れた行は捨てて解析可能な規則だけを使う(RFC 9309)。
func ParseRobots(r io.Reader, agent string) *Robots {
	out := &Robots{}
	agentLower := strings.ToLower(agent)

	// 群は「User-agent 行の並び → 規則の並び」で切れる。規則が来た後の User-agent 行は次の群。
	var currentAgents []string
	var inRules bool
	matched := false     // いま読んでいる群が自分に一致するか
	starMatched := false // `*` の群を読んでいるか
	var starRules []robotsRule
	var starDelay time.Duration

	sc := bufio.NewScanner(io.LimitReader(r, RobotsMaxBytes))
	sc.Buffer(make([]byte, 64*1024), 256*1024)
	for sc.Scan() {
		line := sc.Text()
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			// 空行は群の区切り
			currentAgents = nil
			inRules = false
			matched = false
			starMatched = false
			continue
		}
		field, value, ok := strings.Cut(line, ":")
		if !ok {
			continue // 壊れた行は捨てる
		}
		field = strings.ToLower(strings.TrimSpace(field))
		value = strings.TrimSpace(value)

		switch field {
		case "user-agent":
			if inRules {
				// 規則の後の User-agent は新しい群の始まり
				currentAgents = nil
				inRules = false
				matched = false
				starMatched = false
			}
			v := strings.ToLower(value)
			currentAgents = append(currentAgents, v)
			if v == "*" {
				starMatched = true
			} else if v == agentLower || strings.HasPrefix(agentLower, v) {
				// RFC 9309: product token は大文字小文字を無視して比較する。
				// 実運用では "Googlebot" が "Googlebot-Image" にも当たるので前方一致も採る
				matched = true
			}
		case "allow", "disallow":
			inRules = true
			rule, ok := makeRule(value, field == "allow")
			if !ok {
				continue
			}
			if matched {
				out.rules = append(out.rules, rule)
				out.Groups++
			} else if starMatched {
				starRules = append(starRules, rule)
			}
		case "crawl-delay":
			inRules = true
			if f, err := strconv.ParseFloat(value, 64); err == nil && f > 0 {
				d := time.Duration(f * float64(time.Second))
				if matched {
					out.CrawlDelay = d
				} else if starMatched {
					starDelay = d
				}
			}
		case "sitemap":
			// Sitemap は群に属さない(RFC 9309 §2.2.4)
			if value != "" {
				out.Sitemaps = append(out.Sitemaps, value)
			}
		}
		_ = currentAgents
	}

	// 自分に一致する群が一つも無ければ `*` の群に従う
	if out.Groups == 0 {
		out.rules = starRules
		out.CrawlDelay = starDelay
		out.Groups = len(starRules)
	}
	return out
}

// makeRule はパターン文字列から規則を作る。値が空の Disallow は「何も拒否しない」なので捨てる。
func makeRule(value string, allow bool) (robotsRule, bool) {
	if value == "" {
		return robotsRule{}, false
	}
	p := decodePath(value)
	return robotsRule{pattern: p, allow: allow, length: len(p)}, true
}

// decodePath は percent-encoding を復号して比較の土俵を揃える。
// robots.txt 側と URL 側で符号化が違っても同じ資源を同じ文字列にするため(RFC 9309 §2.2.2)。
// 復号できない文字列はそのまま返す。
func decodePath(p string) string {
	if !strings.Contains(p, "%") {
		return p
	}
	// `*` と `$` は復号の対象外なので、いったん退避せずとも安全(percent 記法に現れない)
	if d, err := url.PathUnescape(p); err == nil {
		return d
	}
	return p
}

// Allows は path(クエリを含んでよい)がこの群の規則で許されるかを返す。
// 一致が無ければ許可(RFC 9309: 「規則に当たらなければ URI は許される」)。
func (r *Robots) Allows(path string) bool {
	if r == nil || len(r.rules) == 0 {
		return true
	}
	target := decodePath(path)
	best := robotsRule{}
	found := false
	for _, rule := range r.rules {
		if !matchPattern(rule.pattern, target) {
			continue
		}
		switch {
		case !found:
			best, found = rule, true
		case rule.length > best.length:
			best = rule
		case rule.length == best.length && rule.allow:
			// 同じ長さなら Allow を採る(RFC 9309: 「同等なら allow を使うべき」)
			best = rule
		}
	}
	if !found {
		return true
	}
	return best.allow
}

// AllowsURL は絶対 URL を受け取り、パス+クエリで判定する。
func (r *Robots) AllowsURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return true
	}
	p := u.EscapedPath()
	if p == "" {
		p = "/"
	}
	if u.RawQuery != "" {
		p += "?" + u.RawQuery
	}
	return r.Allows(p)
}

// matchPattern は robots.txt のパターン(前方一致・`*` は 0 個以上の任意文字・末尾 `$` は終端)で
// path に当たるかを返す。正規表現に変換せず、素直な後戻り照合で実装する
// (パターンは短く、正規表現へ変換するとメタ文字の逸出を自分で塞ぐことになるため)。
func matchPattern(pattern, path string) bool {
	anchored := strings.HasSuffix(pattern, "$")
	if anchored {
		pattern = pattern[:len(pattern)-1]
	}
	return matchHere(pattern, path, anchored)
}

func matchHere(pattern, path string, anchored bool) bool {
	for {
		if pattern == "" {
			// パターンを使い切った。終端指定があれば path も尽きている必要がある
			return !anchored || path == ""
		}
		if pattern[0] == '*' {
			rest := pattern[1:]
			// `*` は 0 文字以上に当たる。残りをどの位置から当てても良い
			for i := 0; i <= len(path); i++ {
				if matchHere(rest, path[i:], anchored) {
					return true
				}
			}
			return false
		}
		if path == "" || pattern[0] != path[0] {
			return false
		}
		pattern = pattern[1:]
		path = path[1:]
	}
}
