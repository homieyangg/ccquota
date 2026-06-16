package ingest

import (
	"crypto/subtle"
	"net/http"
)

// bearerOK 檢查 Authorization: Bearer <token> 是否等於 tok(常數時間比較)。
// tok 為空時一律不通過(ingest 停用)。供 JSON 推送端點(usage / token)共用。
func bearerOK(tok []byte, r *http.Request) bool {
	if len(tok) == 0 {
		return false
	}
	const prefix = "Bearer "
	bearer := r.Header.Get("Authorization")
	if len(bearer) <= len(prefix) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(bearer[len(prefix):]), tok) == 1
}
