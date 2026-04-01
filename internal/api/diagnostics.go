package api

import (
	"encoding/json"
	"net/http"
	"runtime"
	"time"
)

type RuntimeStatsResponse struct {
	TimestampUTC  string `json:"timestampUtc"`
	NumGoroutine  int    `json:"numGoroutine"`
	AllocBytes    uint64 `json:"allocBytes"`
	TotalAlloc    uint64 `json:"totalAllocBytes"`
	SysBytes      uint64 `json:"sysBytes"`
	HeapAlloc     uint64 `json:"heapAllocBytes"`
	HeapInuse     uint64 `json:"heapInuseBytes"`
	HeapObjects   uint64 `json:"heapObjects"`
	StackInuse    uint64 `json:"stackInuseBytes"`
	NextGC        uint64 `json:"nextGCBytes"`
	LastGCTimeUTC string `json:"lastGCTimeUtc"`
}

func RuntimeStatsHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)

		lastGC := ""
		if ms.LastGC > 0 {
			lastGC = time.Unix(0, int64(ms.LastGC)).UTC().Format(time.RFC3339Nano)
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(RuntimeStatsResponse{
			TimestampUTC:  time.Now().UTC().Format(time.RFC3339Nano),
			NumGoroutine:  runtime.NumGoroutine(),
			AllocBytes:    ms.Alloc,
			TotalAlloc:    ms.TotalAlloc,
			SysBytes:      ms.Sys,
			HeapAlloc:     ms.HeapAlloc,
			HeapInuse:     ms.HeapInuse,
			HeapObjects:   ms.HeapObjects,
			StackInuse:    ms.StackInuse,
			NextGC:        ms.NextGC,
			LastGCTimeUTC: lastGC,
		})
	}
}
