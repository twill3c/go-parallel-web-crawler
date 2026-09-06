package crawler

import "sync"

// URLSet は「もう見た URL」の集合。複数の Worker / manager から同時に触られるので
// sync.Mutex で守る(SPEC F-05 / 原本 §12)。
//
// 上限 limit 件で受付を止める(D-06)。jobs channel の容量を maxPages にしておけば、
// URLSet が maxPages 件で止まる限り manager は jobs への送信でブロックしない —
// 送受信の相互待ちによるデッドロックを構造で排除している。
type URLSet struct {
	mu    sync.Mutex
	seen  map[string]struct{}
	limit int
}

// NewURLSet は上限 limit 件の空集合を作る。limit <= 0 は無制限。
func NewURLSet(limit int) *URLSet {
	return &URLSet{seen: make(map[string]struct{}), limit: limit}
}

// Add は u を集合に入れ、**このとき初めて入った**ときだけ true を返す。
// 既知の URL、または上限に達しているときは false。
//
//	Lock → 既に存在? → Yes: false / No: 上限? → Yes: false / No: 追加して true → Unlock
func (s *URLSet) Add(u string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.seen[u]; ok {
		return false
	}
	if s.limit > 0 && len(s.seen) >= s.limit {
		return false
	}
	s.seen[u] = struct{}{}
	return true
}

// Has は u が既知かを返す。
func (s *URLSet) Has(u string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.seen[u]
	return ok
}

// Len は集合の大きさ。
func (s *URLSet) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.seen)
}
