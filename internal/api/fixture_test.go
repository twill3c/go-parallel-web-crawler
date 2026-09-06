package api

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestRecordFixtures は画面のテスト(tests/ui/reducer.test.mjs・T-401)が読む SSE の実録を書き出す。
// 実サーバの出力そのもの(配布形)をフィクスチャにするため(HC-139 / HC-179(b))。
// 通常は skip。再録は `GPWC_RECORD_FIXTURES=1 go test ./internal/api -run TestRecordFixtures`。
func TestRecordFixtures(t *testing.T) {
	if os.Getenv("GPWC_RECORD_FIXTURES") == "" {
		t.Skip("GPWC_RECORD_FIXTURES が無いので skip")
	}
	dir := filepath.Join("..", "..", "tests", "ui", "fixtures")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name    string
		n       int
		workers int
		max     int
		delay   time.Duration
	}{
		{"small_w2", 4, 2, 10, 0},
		{"capped_w5", 40, 5, 12, 0},
		{"single_worker", 6, 1, 50, 0},
	}
	for _, c := range cases {
		target := targetSite(t, c.n, c.delay)
		_, api := apiServer(t)
		resp := postCrawl(t, api, fmt.Sprintf(`{"url":%q,"workers":%d,"maxPages":%d,"requestDelayMs":0}`, target.URL, c.workers, c.max))
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, c.name+".sse")
		if err := os.WriteFile(path, body, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s (%d bytes)", path, len(body))
	}
}
