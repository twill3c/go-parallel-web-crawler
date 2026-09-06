// Package api は HTTP ハンドラを置く。クローラ本体(internal/crawler)と画面(public/)の間に立つ薄い層。
package api

import (
	"encoding/json"
	"net/http"
)

// Health は GET /api/health(F-20)。稼働確認のためだけの固定応答。
func Health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// writeJSON は JSON 応答の共通経路。Content-Type を固定し、エンコード失敗は 500 にする。
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
