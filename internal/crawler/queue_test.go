package crawler

import (
	"sync"
	"sync/atomic"
	"testing"
)

// T-107 / F-05, G-04: URLSet.Add を 100 goroutine から同一 URL で同時に呼ぶと true は正確に 1 回。
// -race で走らせる(TEST_SPEC 実行規約)。期待値の出所: SPEC F-05「同一 URL を二度取得しない」。
func TestURLSet_ConcurrentAdd(t *testing.T) {
	const n = 100
	set := NewURLSet(1000)
	var wg sync.WaitGroup
	var start sync.WaitGroup
	start.Add(1)
	var trues int32
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			start.Wait() // 全 goroutine を揃えてから一斉に Add する
			if set.Add("https://example.com/same") {
				atomic.AddInt32(&trues, 1)
			}
		}()
	}
	start.Done()
	wg.Wait()
	if trues != 1 {
		t.Fatalf("Add が true を返した回数 = %d, want 1", trues)
	}
	if set.Len() != 1 {
		t.Fatalf("Len = %d, want 1", set.Len())
	}
}

// T-108 / F-05, D-06: 上限 N 件で Add を拒む。N+1 件目が false・Len()==N。
// D-06 の根拠: jobs channel の容量 = maxPages なので URLSet が maxPages で止まれば送信はブロックしない。
func TestURLSet_Limit(t *testing.T) {
	const limit = 5
	set := NewURLSet(limit)
	for i := 0; i < limit; i++ {
		if !set.Add(urlN(i)) {
			t.Fatalf("%d 件目が拒まれた", i+1)
		}
	}
	if set.Add(urlN(limit)) {
		t.Fatal("上限を超えて Add が通った")
	}
	if set.Len() != limit {
		t.Fatalf("Len = %d, want %d", set.Len(), limit)
	}
	// 既知の URL は上限到達後も「既知」であり、二度目の Add は false(重複と上限を区別しない)
	if set.Add(urlN(0)) {
		t.Fatal("既知 URL の再 Add が true")
	}
	if !set.Has(urlN(0)) || set.Has(urlN(limit)) {
		t.Fatal("Has の判定が集合と食い違う")
	}
}

func urlN(i int) string { return "https://example.com/p" + string(rune('a'+i)) }
