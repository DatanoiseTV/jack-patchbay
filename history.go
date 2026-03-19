package main

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

// TimeSeriesSample stores one snapshot of system health
type TimeSeriesSample struct {
	Timestamp int64   `json:"t"`       // unix ms
	Xruns     int     `json:"xruns"`   // cumulative xrun count
	DSPLoad   float32 `json:"dspLoad"` // JACK CPU load (0-100%)
	Clients   int     `json:"clients"` // number of JACK clients
}

// XrunEvent stores individual xrun occurrences
type XrunEvent struct {
	Timestamp int64 `json:"t"`
	Total     int   `json:"total"` // cumulative count at time of event
}

const (
	maxSamples   = 3600 // 1 sample/sec = 1 hour of history
	maxXrunLog   = 1000 // last 1000 xrun events
)

var (
	histMu      sync.RWMutex
	histSamples []TimeSeriesSample
	histXruns   []XrunEvent
	lastXrunCnt int
)

func historyLoop() {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		if !cIsActive() {
			continue
		}

		now := time.Now().UnixMilli()
		xruns := cGetXruns()
		dspLoad := cGetDSPLoad()

		stateMu.RLock()
		nClients := 0
		if lastState != nil {
			nClients = len(lastState.Clients)
		}
		stateMu.RUnlock()

		sample := TimeSeriesSample{
			Timestamp: now,
			Xruns:     xruns,
			DSPLoad:   dspLoad,
			Clients:   nClients,
		}

		histMu.Lock()
		histSamples = append(histSamples, sample)
		if len(histSamples) > maxSamples {
			histSamples = histSamples[len(histSamples)-maxSamples:]
		}

		// Log individual xrun events
		if xruns > lastXrunCnt {
			for i := 0; i < xruns-lastXrunCnt; i++ {
				histXruns = append(histXruns, XrunEvent{Timestamp: now, Total: xruns})
			}
			if len(histXruns) > maxXrunLog {
				histXruns = histXruns[len(histXruns)-maxXrunLog:]
			}
		}
		lastXrunCnt = xruns
		histMu.Unlock()
	}
}

type HistoryResponse struct {
	Samples []TimeSeriesSample `json:"samples"`
	Xruns   []XrunEvent        `json:"xruns"`
}

func apiHistory(w http.ResponseWriter, r *http.Request) {
	// Optional: ?since=<unix_ms> to get only recent data
	histMu.RLock()
	resp := HistoryResponse{
		Samples: histSamples,
		Xruns:   histXruns,
	}
	histMu.RUnlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
