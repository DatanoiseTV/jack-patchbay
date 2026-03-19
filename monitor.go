package main

import (
	"encoding/json"
	"log"
	"math"
	"net/http"
	"sync"
	"time"

	"github.com/pion/webrtc/v3"
	"github.com/pion/webrtc/v3/pkg/media"
	"gopkg.in/hraban/opus.v2"
)

var (
	monMu     sync.Mutex
	monPC_    *webrtc.PeerConnection
	monCancel chan struct{}
)

type MonitorRequest struct {
	SDP      string `json:"sdp"`
	Channels []int  `json:"channels"`
	Bitrate  int    `json:"bitrate"` // kbps: 32 or 64 (default 64)
}
type MonitorResponse struct {
	SDP string `json:"sdp"`
}

var monBitrate int = 64000 // current bitrate in bps

func apiMonitorStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "POST", 405)
		return
	}
	var req MonitorRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if len(req.Channels) == 0 {
		http.Error(w, "need at least one channel", 400)
		return
	}

	stopMonitorInternal()

	offer := webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: req.SDP}
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		http.Error(w, "peer connection: "+err.Error(), 500)
		return
	}

	track, err := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus},
		"monitor", "patchbay",
	)
	if err != nil {
		pc.Close()
		http.Error(w, "track: "+err.Error(), 500)
		return
	}
	if _, err := pc.AddTrack(track); err != nil {
		pc.Close()
		http.Error(w, "add track: "+err.Error(), 500)
		return
	}

	if err := pc.SetRemoteDescription(offer); err != nil {
		pc.Close()
		http.Error(w, "set remote: "+err.Error(), 500)
		return
	}

	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		pc.Close()
		http.Error(w, "create answer: "+err.Error(), 500)
		return
	}
	if err := pc.SetLocalDescription(answer); err != nil {
		pc.Close()
		http.Error(w, "set local: "+err.Error(), 500)
		return
	}

	// Wait for ICE gathering
	gatherDone := webrtc.GatheringCompletePromise(pc)
	select {
	case <-gatherDone:
	case <-time.After(5 * time.Second):
		pc.Close()
		http.Error(w, "ICE timeout", 500)
		return
	}

	ch0 := req.Channels[0]
	ch1 := -1
	if len(req.Channels) > 1 {
		ch1 = req.Channels[1]
	}
	// Set bitrate (default 64kbps)
	if req.Bitrate == 32 {
		monBitrate = 32000
	} else {
		monBitrate = 64000
	}

	cMonStart(ch0, ch1)

	cancel := make(chan struct{})
	monMu.Lock()
	monPC_ = pc
	monCancel = cancel
	monMu.Unlock()

	go streamAudioToWebRTC(track, cancel, ch1 >= 0)

	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		log.Printf("WebRTC monitor: %s", state)
		if state == webrtc.PeerConnectionStateFailed ||
			state == webrtc.PeerConnectionStateDisconnected ||
			state == webrtc.PeerConnectionStateClosed {
			stopMonitorInternal()
		}
	})

	resp := MonitorResponse{SDP: pc.LocalDescription().SDP}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func apiMonitorStop(w http.ResponseWriter, r *http.Request) {
	stopMonitorInternal()
	w.WriteHeader(200)
}

func apiMonitorBitrate(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" { http.Error(w, "POST", 405); return }
	var req struct{ Bitrate int `json:"bitrate"` }
	json.NewDecoder(r.Body).Decode(&req)
	if req.Bitrate == 32 {
		monBitrate = 32000
	} else {
		monBitrate = 64000
	}
	w.WriteHeader(200)
}

func stopMonitorInternal() {
	monMu.Lock()
	defer monMu.Unlock()
	if monCancel != nil {
		close(monCancel)
		monCancel = nil
	}
	if monPC_ != nil {
		monPC_.Close()
		monPC_ = nil
	}
	cMonStop()
}

func streamAudioToWebRTC(track *webrtc.TrackLocalStaticSample, cancel chan struct{}, stereo bool) {
	channels := 1
	if stereo {
		channels = 2
	}
	enc, err := opus.NewEncoder(48000, channels, opus.AppAudio)
	if err != nil {
		log.Printf("Opus encoder error: %v", err)
		return
	}
	enc.SetBitrate(monBitrate)

	const (
		frameSamples = 960     // 20ms @ 48kHz per Opus frame
		ringSize     = 16384   // must match C MON_RING_SIZE
		targetBuffer = 960 * 3 // 60ms target buffer level
		maxBuffer    = 960 * 8 // 160ms max before skip-ahead
	)

	pcmBuf := make([]int16, frameSamples*channels)
	opusBuf := make([]byte, 4000)
	outL := make([]float32, 4096)
	outR := make([]float32, 4096)

	// Start reading from a position that gives us ~60ms of pre-buffered audio
	writePos := cMonGetWritePos()
	readPos := (writePos - targetBuffer + ringSize) & (ringSize - 1)
	primed := false

	frameDuration := 20 * time.Millisecond
	ticker := time.NewTicker(frameDuration)
	defer ticker.Stop()

	for {
		select {
		case <-cancel:
			return
		case <-ticker.C:
		}

		if !cIsActive() {
			continue
		}

		// Calculate how much audio is available in the ring buffer
		writePos = cMonGetWritePos()
		avail := (writePos - readPos + ringSize) & (ringSize - 1)

		// Pre-buffer: wait until we have enough before starting
		if !primed {
			if avail < targetBuffer {
				continue
			}
			primed = true
			log.Printf("WebRTC monitor: primed with %d samples (%.1fms)", avail, float64(avail)/48.0)
		}

		// If buffer grew too large (clock drift), skip ahead to target level
		if avail > maxBuffer {
			skip := avail - targetBuffer
			readPos = (readPos + skip) & (ringSize - 1)
			avail = targetBuffer
		}

		// If not enough for a full frame, send silence (underrun)
		if avail < frameSamples {
			for i := range pcmBuf {
				pcmBuf[i] = 0
			}
		} else {
			// Read one Opus frame worth of audio
			n := cMonRead(readPos, outL, outR, frameSamples)
			readPos = (readPos + frameSamples) & (ringSize - 1)

			for i := 0; i < frameSamples; i++ {
				if i < n {
					if channels == 2 {
						pcmBuf[i*2] = floatToI16(float64(outL[i]))
						pcmBuf[i*2+1] = floatToI16(float64(outR[i]))
					} else {
						pcmBuf[i] = floatToI16(float64(outL[i]))
					}
				} else {
					if channels == 2 {
						pcmBuf[i*2] = 0
						pcmBuf[i*2+1] = 0
					} else {
						pcmBuf[i] = 0
					}
				}
			}
		}

		nBytes, err := enc.Encode(pcmBuf, opusBuf)
		if err != nil || nBytes == 0 {
			continue
		}

		if err := track.WriteSample(media.Sample{
			Data:     opusBuf[:nBytes],
			Duration: frameDuration,
		}); err != nil {
			return
		}
	}
}

func floatToI16(f float64) int16 {
	f *= 32767.0
	if f > 32767 {
		f = 32767
	} else if f < -32768 {
		f = -32768
	}
	return int16(math.Round(f))
}
