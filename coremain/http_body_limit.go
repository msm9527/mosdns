package coremain

import "net/http"

const maxJSONBodyBytes int64 = 10 << 20 // 10 MiB

func limitJSONBody(w http.ResponseWriter, r *http.Request) {
	if r == nil || r.Body == nil || r.Body == http.NoBody {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBodyBytes)
}
