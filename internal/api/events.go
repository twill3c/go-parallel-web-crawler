package api

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/twill3c/go-parallel-web-crawler/internal/model"
)

// sseWriter は Server-Sent Events(text/event-stream)の書き手。
//
//	event: page_completed
//	data: {"type":"page_completed", ...}
//	(空行)
//
// http.Flusher があれば 1 件ごとに Flush する。無い環境(ResponseWriter がバッファする
// プロキシ・ランタイム)では応答完了時に一括で届く。画面はどちらでも同じ最終状態になる(F-36)。
type sseWriter struct {
	w       http.ResponseWriter
	flusher http.Flusher
}

func newSSEWriter(w http.ResponseWriter) *sseWriter {
	f, _ := w.(http.Flusher)
	return &sseWriter{w: w, flusher: f}
}

// send は 1 イベントを書く。書き込みの失敗(切断)は無視する — 切断は ctx の cancel として
// クローラに伝わり、そちらで畳まれる。
func (s *sseWriter) send(e model.Event) {
	data, err := json.Marshal(e)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", e.Type, data)
	if s.flusher != nil {
		s.flusher.Flush()
	}
}
