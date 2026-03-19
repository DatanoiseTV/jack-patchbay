package main

/*
#cgo LDFLAGS: -ljack -lm
#include <jack/jack.h>
#include <stdlib.h>
#include <string.h>
#include <math.h>

#define MAX_PORTS 256
#define FFT_SIZE 512
#define FFT_BINS (FFT_SIZE / 2)

// --- Simple radix-2 FFT ---
typedef struct { float re, im; } cpx_t;

static void fft(cpx_t *x, int n) {
    // Bit-reversal permutation
    for (int i = 1, j = 0; i < n; i++) {
        int bit = n >> 1;
        for (; j & bit; bit >>= 1) j ^= bit;
        j ^= bit;
        if (i < j) { cpx_t tmp = x[i]; x[i] = x[j]; x[j] = tmp; }
    }
    // Cooley-Tukey
    for (int len = 2; len <= n; len <<= 1) {
        float ang = -2.0f * M_PI / len;
        cpx_t wn = { cosf(ang), sinf(ang) };
        for (int i = 0; i < n; i += len) {
            cpx_t w = {1.0f, 0.0f};
            for (int j = 0; j < len / 2; j++) {
                cpx_t u = x[i + j];
                cpx_t v = { w.re * x[i + j + len/2].re - w.im * x[i + j + len/2].im,
                            w.re * x[i + j + len/2].im + w.im * x[i + j + len/2].re };
                x[i + j].re = u.re + v.re;
                x[i + j].im = u.im + v.im;
                x[i + j + len/2].re = u.re - v.re;
                x[i + j + len/2].im = u.im - v.im;
                float tmp = w.re * wn.re - w.im * wn.im;
                w.im = w.re * wn.im + w.im * wn.re;
                w.re = tmp;
            }
        }
    }
}

// Hanning window (precomputed)
static float hann_window[FFT_SIZE];
static int hann_init = 0;
static void init_hann() {
    if (hann_init) return;
    for (int i = 0; i < FFT_SIZE; i++)
        hann_window[i] = 0.5f * (1.0f - cosf(2.0f * M_PI * i / (FFT_SIZE - 1)));
    hann_init = 1;
}

// --- Meter + FFT state ---
typedef struct {
    jack_client_t *client;
    char port_names[MAX_PORTS][256];
    float peaks[MAX_PORTS];
    float rms_acc[MAX_PORTS];
    float rms[MAX_PORTS];
    int   rms_count[MAX_PORTS];
    int   port_count;
    int   active;

    // FFT ring buffers and results
    float fft_ring[MAX_PORTS][FFT_SIZE];  // circular sample buffer per port
    int   fft_pos[MAX_PORTS];              // write position in ring
    float fft_mag[MAX_PORTS][FFT_BINS];    // last computed magnitude spectrum (dB)
    int   fft_sub[MAX_PORTS];              // subscription flag (1=compute FFT)
    int   fft_ready[MAX_PORTS];            // new FFT data available
} meter_state_t;

static meter_state_t g_meter;
static cpx_t fft_work[FFT_SIZE]; // scratch buffer (only used in process callback, single-threaded)

static int process_callback(jack_nframes_t nframes, void *arg) {
    meter_state_t *m = (meter_state_t *)arg;
    init_hann();

    for (int i = 0; i < m->port_count; i++) {
        jack_port_t *port = jack_port_by_name(m->client, m->port_names[i]);
        if (!port) { m->peaks[i] *= 0.9f; m->rms[i] *= 0.95f; continue; }
        float *buf = (float *)jack_port_get_buffer(port, nframes);
        if (!buf) { m->peaks[i] *= 0.9f; m->rms[i] *= 0.95f; continue; }

        float peak = 0.0f;
        float sum_sq = 0.0f;
        for (jack_nframes_t j = 0; j < nframes; j++) {
            float v = buf[j];
            float a = fabsf(v);
            if (a > peak) peak = a;
            sum_sq += v * v;

            // Accumulate into FFT ring buffer if subscribed
            if (m->fft_sub[i]) {
                m->fft_ring[i][m->fft_pos[i]] = v;
                m->fft_pos[i] = (m->fft_pos[i] + 1) & (FFT_SIZE - 1);
            }
        }

        // Peak
        if (peak > m->peaks[i]) m->peaks[i] = peak;
        else m->peaks[i] = m->peaks[i] * 0.85f + peak * 0.15f;

        // RMS (~300ms window)
        m->rms_acc[i] += sum_sq;
        m->rms_count[i] += nframes;
        if (m->rms_count[i] >= 14400) {
            m->rms[i] = sqrtf(m->rms_acc[i] / (float)m->rms_count[i]);
            m->rms_acc[i] = 0.0f;
            m->rms_count[i] = 0;
        }

        // Compute FFT when subscribed and enough samples accumulated
        if (m->fft_sub[i] && m->fft_pos[i] == 0) {
            // Copy ring buffer with Hanning window into work buffer
            for (int k = 0; k < FFT_SIZE; k++) {
                fft_work[k].re = m->fft_ring[i][k] * hann_window[k];
                fft_work[k].im = 0.0f;
            }
            fft(fft_work, FFT_SIZE);
            // Compute magnitude in dB
            for (int k = 0; k < FFT_BINS; k++) {
                float mag = sqrtf(fft_work[k].re * fft_work[k].re + fft_work[k].im * fft_work[k].im);
                mag /= (float)FFT_SIZE; // normalize
                if (mag < 1e-7f) mag = 1e-7f;
                m->fft_mag[i][k] = 20.0f * log10f(mag);
            }
            m->fft_ready[i] = 1;
        }
    }
    return 0;
}

static void on_jack_shutdown(void *arg) {
    meter_state_t *m = (meter_state_t *)arg;
    m->active = 0; m->client = NULL; m->port_count = 0;
}

static int jack_init() {
    if (g_meter.client) { jack_client_close(g_meter.client); g_meter.client = NULL; }
    g_meter.active = 0; g_meter.port_count = 0;
    memset(g_meter.fft_sub, 0, sizeof(g_meter.fft_sub));
    jack_status_t status;
    g_meter.client = jack_client_open("patchbay-meter", JackNoStartServer, &status);
    if (!g_meter.client) return -1;
    jack_on_shutdown(g_meter.client, on_jack_shutdown, &g_meter);
    jack_set_process_callback(g_meter.client, process_callback, &g_meter);
    if (jack_activate(g_meter.client)) return -2;
    g_meter.active = 1;
    return 0;
}

static void jack_cleanup() {
    if (g_meter.client) { jack_deactivate(g_meter.client); jack_client_close(g_meter.client); g_meter.client=NULL; g_meter.active=0; }
}

static int jack_is_active() { return g_meter.active; }

static void jack_update_ports(const char **names, int count) {
    if (count > MAX_PORTS) count = MAX_PORTS;
    for (int i = g_meter.port_count; i < count; i++) {
        g_meter.peaks[i] = 0; g_meter.rms[i] = 0;
        g_meter.rms_acc[i] = 0; g_meter.rms_count[i] = 0;
        g_meter.fft_pos[i] = 0; g_meter.fft_ready[i] = 0; g_meter.fft_sub[i] = 0;
    }
    g_meter.port_count = count;
    for (int i = 0; i < count; i++) {
        strncpy(g_meter.port_names[i], names[i], 255);
        g_meter.port_names[i][255] = '\0';
    }
}

typedef struct { float peak; float rms; } meter_reading_t;

static int jack_read_meters(meter_reading_t *out, int max) {
    int n = g_meter.port_count;
    if (n > max) n = max;
    for (int i = 0; i < n; i++) { out[i].peak = g_meter.peaks[i]; out[i].rms = g_meter.rms[i]; }
    return n;
}

// FFT subscription
static void jack_fft_subscribe(int port_idx, int on) {
    if (port_idx >= 0 && port_idx < MAX_PORTS) {
        g_meter.fft_sub[port_idx] = on;
        if (!on) g_meter.fft_ready[port_idx] = 0;
    }
}

// Read FFT data for a port. Returns number of bins (FFT_BINS) or 0 if not ready.
static int jack_read_fft(int port_idx, float *out, int max) {
    if (port_idx < 0 || port_idx >= g_meter.port_count) return 0;
    if (!g_meter.fft_ready[port_idx]) return 0;
    int n = FFT_BINS;
    if (n > max) n = max;
    for (int i = 0; i < n; i++) out[i] = g_meter.fft_mag[port_idx][i];
    g_meter.fft_ready[port_idx] = 0;
    return n;
}

static int jack_get_fft_bins() { return FFT_BINS; }

static jack_nframes_t meter_get_sample_rate() {
    if (!g_meter.client) return 48000;
    return jack_get_sample_rate(g_meter.client);
}
static jack_nframes_t meter_get_buffer_size() {
    if (!g_meter.client) return 48;
    return jack_get_buffer_size(g_meter.client);
}

typedef struct { char name[256]; int flags; jack_nframes_t latency; } port_info_t;

static int jack_list_ports(port_info_t *out, int max) {
    if (!g_meter.client) return 0;
    const char **ports = jack_get_ports(g_meter.client, NULL, NULL, 0);
    if (!ports) return 0;
    int n = 0;
    for (int i = 0; ports[i] && n < max; i++) {
        strncpy(out[n].name, ports[i], 255); out[n].name[255] = '\0';
        jack_port_t *p = jack_port_by_name(g_meter.client, ports[i]);
        out[n].flags = p ? jack_port_flags(p) : 0;
        if (p) {
            jack_latency_range_t range;
            int mode = (out[n].flags & JackPortIsOutput) ? JackCaptureLatency : JackPlaybackLatency;
            jack_port_get_latency_range(p, mode, &range);
            out[n].latency = range.max;
        } else { out[n].latency = 0; }
        n++;
    }
    jack_free(ports);
    return n;
}

static int jack_port_connections(const char *port_name, char out[][256], int max) {
    if (!g_meter.client) return 0;
    jack_port_t *p = jack_port_by_name(g_meter.client, port_name);
    if (!p) return 0;
    const char **conns = jack_port_get_all_connections(g_meter.client, p);
    if (!conns) return 0;
    int n = 0;
    for (int i = 0; conns[i] && n < max; i++) { strncpy(out[n], conns[i], 255); out[n][255]='\0'; n++; }
    jack_free(conns);
    return n;
}

static int jack_do_connect(const char *s, const char *d) { return g_meter.client ? jack_connect(g_meter.client,s,d) : -1; }
static int jack_do_disconnect(const char *s, const char *d) { return g_meter.client ? jack_disconnect(g_meter.client,s,d) : -1; }
*/
import "C"

import (
	"crypto/sha1"
	"embed"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"math"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
	"unsafe"
)

//go:embed static/*
var staticFS embed.FS

type Port struct {
	Name        string  `json:"name"`
	Client      string  `json:"client"`
	Port        string  `json:"port"`
	IsOut       bool    `json:"isOut"`
	Connections int     `json:"connections"`
	LatencyMs   float64 `json:"latencyMs"`
}
type Connection struct{ Source, Dest string }
type State struct {
	Ports       []Port       `json:"ports"`
	Connections []Connection `json:"connections"`
	Clients     []string     `json:"clients"`
	SampleRate  int          `json:"sampleRate"`
	BufferSize  int          `json:"bufferSize"`
	FFTBins     int          `json:"fftBins"`
}

var (
	stateMu   sync.RWMutex
	lastState *State
	sseMu     sync.Mutex
	sseChans  = make(map[chan []byte]struct{})
	wsMu      sync.Mutex
	wsConns   = make(map[*wsClient]struct{})
)

type wsClient struct {
	conn    net.Conn
	fftSubs map[int]bool // port indices this client wants FFT for
	mu      sync.Mutex
}

func main() {
	addr := flag.String("addr", ":8998", "Listen address")
	meterHz := flag.Int("meter-hz", 30, "Meter update rate")
	fftHz := flag.Int("fft-hz", 15, "FFT/RTA update rate")
	stateHz := flag.Float64("state-hz", 2, "State poll rate")
	flag.Parse()

	log.Printf("JACK Patchbay on %s (meters %dHz, fft %dHz, state %.0fHz)", *addr, *meterHz, *fftHz, *stateHz)
	go jackConnectLoop()
	go statePollLoop(time.Duration(float64(time.Second) / *stateHz))
	go meterLoop(time.Duration(float64(time.Second) / float64(*meterHz)))
	go fftLoop(time.Duration(float64(time.Second) / float64(*fftHz)))

	sub, _ := fs.Sub(staticFS, "static")
	mux := http.NewServeMux()
	mux.HandleFunc("/api/state", apiState)
	mux.HandleFunc("/api/connect", apiConnect)
	mux.HandleFunc("/api/disconnect", apiDisconnect)
	mux.HandleFunc("/api/events", apiSSE)
	mux.HandleFunc("/api/meters", apiMetersWS)
	mux.HandleFunc("/api/ping", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	mux.Handle("/", http.FileServer(http.FS(sub)))

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
		w.Header().Set("Cross-Origin-Embedder-Policy", "require-corp")
		mux.ServeHTTP(w, r)
	})
	log.Fatal(http.ListenAndServe(*addr, handler))
}

func jackConnectLoop() {
	for {
		if C.jack_is_active() == 0 {
			log.Println("Connecting to JACK...")
			if rc := C.jack_init(); rc != 0 {
				log.Printf("JACK connect failed (%d), retrying in 3s", rc)
				time.Sleep(3 * time.Second)
				continue
			}
			log.Println("Connected to JACK")
		}
		time.Sleep(1 * time.Second)
	}
}

func statePollLoop(interval time.Duration) {
	var lastJSON []byte
	for {
		time.Sleep(interval)
		if C.jack_is_active() == 0 { continue }
		st := readState()
		data, _ := json.Marshal(st)
		stateMu.Lock()
		lastState = st
		stateMu.Unlock()
		if string(data) != string(lastJSON) {
			lastJSON = data
			broadcastSSE(data)
		}
	}
}

func linToDb(lin float64) float64 {
	if lin < 1e-5 { return -96.0 }
	db := 20.0 * math.Log10(lin)
	if db < -96.0 { return -96.0 }
	if db > 24.0 { return 24.0 }
	return db
}

func dbToInt16(db float64) int16 {
	v := db * 100.0
	if v < -32768 { return -32768 }
	if v > 32767 { return 32767 }
	return int16(v)
}

// meterLoop: type 0x01 — peak + RMS
func meterLoop(interval time.Duration) {
	var readings [256]C.meter_reading_t
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for range ticker.C {
		if C.jack_is_active() == 0 { continue }
		stateMu.RLock()
		st := lastState
		stateMu.RUnlock()
		if st == nil || len(st.Ports) == 0 { continue }

		n := int(C.jack_read_meters(&readings[0], 256))
		if n > len(st.Ports) { n = len(st.Ports) }

		buf := make([]byte, 6+n*4)
		buf[0] = 0x01
		buf[1] = byte(n)
		binary.LittleEndian.PutUint32(buf[2:], uint32(time.Now().UnixMilli()))
		off := 6
		for i := 0; i < n; i++ {
			binary.LittleEndian.PutUint16(buf[off:], uint16(dbToInt16(linToDb(float64(readings[i].peak)))))
			off += 2
		}
		for i := 0; i < n; i++ {
			binary.LittleEndian.PutUint16(buf[off:], uint16(dbToInt16(linToDb(float64(readings[i].rms)))))
			off += 2
		}
		broadcastWS(buf, false) // send to all clients
	}
}

// fftLoop: type 0x02 — per-port FFT spectrum, only for subscribed ports
func fftLoop(interval time.Duration) {
	var fftBuf [256]C.float
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for range ticker.C {
		if C.jack_is_active() == 0 { continue }
		stateMu.RLock()
		st := lastState
		stateMu.RUnlock()
		if st == nil { continue }

		nBins := int(C.jack_get_fft_bins())

		// Collect subscribed port indices across all clients
		wsMu.Lock()
		activeSubs := make(map[int]bool)
		for c := range wsConns {
			c.mu.Lock()
			for idx := range c.fftSubs {
				activeSubs[idx] = true
			}
			c.mu.Unlock()
		}
		wsMu.Unlock()

		// Read and send FFT for each subscribed port
		for portIdx := range activeSubs {
			n := int(C.jack_read_fft(C.int(portIdx), &fftBuf[0], C.int(nBins)))
			if n == 0 { continue }

			// Pack: [0x02] [portIdx:u8] [binCount:u16le] [timestamp:u32le] [flags:u8] [mags:i16le×N]
			frameSize := 8 + n*2
			frame := make([]byte, frameSize)
			frame[0] = 0x02
			frame[1] = byte(portIdx)
			binary.LittleEndian.PutUint16(frame[2:], uint16(n))
			binary.LittleEndian.PutUint32(frame[4:], uint32(time.Now().UnixMilli()))
			off := 8
			for i := 0; i < n; i++ {
				db := float64(fftBuf[i])
				if db < -96.0 { db = -96.0 }
				if db > 24.0 { db = 24.0 }
				binary.LittleEndian.PutUint16(frame[off:], uint16(dbToInt16(db)))
				off += 2
			}

			// Send only to clients subscribed to this port
			wsMu.Lock()
			wsFrame := wsFrameEncode(frame)
			for c := range wsConns {
				c.mu.Lock()
				if c.fftSubs[portIdx] {
					c.conn.SetWriteDeadline(time.Now().Add(50 * time.Millisecond))
					c.conn.Write(wsFrame)
				}
				c.mu.Unlock()
			}
			wsMu.Unlock()
		}
	}
}

func readState() *State {
	var infos [256]C.port_info_t
	n := int(C.jack_list_ports(&infos[0], 256))
	st := &State{FFTBins: int(C.jack_get_fft_bins())}
	clientSet := map[string]bool{}
	sampleRate := float64(C.meter_get_sample_rate())

	for i := 0; i < n; i++ {
		name := C.GoString(&infos[i].name[0])
		parts := strings.SplitN(name, ":", 2)
		if len(parts) != 2 || strings.Contains(parts[0], " ") { continue }
		isOut := (infos[i].flags & C.JackPortIsOutput) != 0
		latMs := float64(infos[i].latency) / sampleRate * 1000.0

		var conns [64][256]C.char
		cName := C.CString(name)
		nConns := int(C.jack_port_connections(cName, &conns[0], 64))
		C.free(unsafe.Pointer(cName))
		for j := 0; j < nConns; j++ {
			dest := C.GoString(&conns[j][0])
			if isOut { st.Connections = append(st.Connections, Connection{Source: name, Dest: dest}) }
		}
		clientSet[parts[0]] = true
		st.Ports = append(st.Ports, Port{
			Name: name, Client: parts[0], Port: parts[1],
			IsOut: isOut, Connections: nConns, LatencyMs: math.Round(latMs*10) / 10,
		})
	}

	sort.Slice(st.Ports, func(i, j int) bool {
		if st.Ports[i].Client != st.Ports[j].Client { return st.Ports[i].Client < st.Ports[j].Client }
		return naturalLess(st.Ports[i].Port, st.Ports[j].Port)
	})
	cNames := make([]*C.char, len(st.Ports))
	for i, p := range st.Ports { cNames[i] = C.CString(p.Name) }
	if len(cNames) > 0 { C.jack_update_ports(&cNames[0], C.int(len(cNames))) }
	for _, c := range cNames { C.free(unsafe.Pointer(c)) }
	for c := range clientSet { st.Clients = append(st.Clients, c) }
	sort.Strings(st.Clients)
	st.SampleRate = int(C.meter_get_sample_rate())
	st.BufferSize = int(C.meter_get_buffer_size())
	return st
}

func naturalLess(a, b string) bool {
	ai, bi := 0, 0
	for ai < len(a) && bi < len(b) {
		ca, cb := a[ai], b[bi]
		if ca >= '0' && ca <= '9' && cb >= '0' && cb <= '9' {
			na, nb := 0, 0
			for ai < len(a) && a[ai] >= '0' && a[ai] <= '9' { na = na*10 + int(a[ai]-'0'); ai++ }
			for bi < len(b) && b[bi] >= '0' && b[bi] <= '9' { nb = nb*10 + int(b[bi]-'0'); bi++ }
			if na != nb { return na < nb }
			continue
		}
		if ca != cb { return ca < cb }
		ai++; bi++
	}
	return len(a) < len(b)
}

// --- HTTP ---

func apiState(w http.ResponseWriter, r *http.Request) {
	stateMu.RLock(); s := lastState; stateMu.RUnlock()
	if s == nil { http.Error(w, "not ready", 503); return }
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s)
}

func apiConnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" { http.Error(w, "POST", 405); return }
	var c Connection; json.NewDecoder(r.Body).Decode(&c)
	src, dst := C.CString(c.Source), C.CString(c.Dest)
	rc := C.jack_do_connect(src, dst); C.free(unsafe.Pointer(src)); C.free(unsafe.Pointer(dst))
	if rc != 0 { http.Error(w, fmt.Sprintf("connect failed: %d", rc), 500); return }
	w.WriteHeader(200)
}

func apiDisconnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" { http.Error(w, "POST", 405); return }
	var c Connection; json.NewDecoder(r.Body).Decode(&c)
	src, dst := C.CString(c.Source), C.CString(c.Dest)
	rc := C.jack_do_disconnect(src, dst); C.free(unsafe.Pointer(src)); C.free(unsafe.Pointer(dst))
	if rc != 0 { http.Error(w, fmt.Sprintf("disconnect failed: %d", rc), 500); return }
	w.WriteHeader(200)
}

// --- SSE ---

func apiSSE(w http.ResponseWriter, r *http.Request) {
	f, ok := w.(http.Flusher)
	if !ok { http.Error(w, "no streaming", 500); return }
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	ch := make(chan []byte, 3)
	sseMu.Lock(); sseChans[ch] = struct{}{}; sseMu.Unlock()
	defer func() { sseMu.Lock(); delete(sseChans, ch); sseMu.Unlock() }()

	stateMu.RLock()
	if lastState != nil { d, _ := json.Marshal(lastState); fmt.Fprintf(w, "data: %s\n\n", d); f.Flush() }
	stateMu.RUnlock()

	for {
		select {
		case d := <-ch: fmt.Fprintf(w, "data: %s\n\n", d); f.Flush()
		case <-r.Context().Done(): return
		}
	}
}

func broadcastSSE(data []byte) {
	sseMu.Lock(); defer sseMu.Unlock()
	for ch := range sseChans { select { case ch <- data: default: } }
}

// --- WebSocket ---

func apiMetersWS(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Upgrade") != "websocket" { http.Error(w, "websocket required", 400); return }
	key := r.Header.Get("Sec-WebSocket-Key")
	if key == "" { http.Error(w, "missing key", 400); return }
	h := sha1.New()
	h.Write([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	accept := base64.StdEncoding.EncodeToString(h.Sum(nil))

	hj, ok := w.(http.Hijacker)
	if !ok { http.Error(w, "no hijack", 500); return }
	conn, bufrw, err := hj.Hijack()
	if err != nil { return }
	bufrw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " + accept + "\r\n\r\n")
	bufrw.Flush()

	client := &wsClient{conn: conn, fftSubs: make(map[int]bool)}
	wsMu.Lock(); wsConns[client] = struct{}{}; wsMu.Unlock()

	// Read loop: handle control messages + detect close
	go func() {
		buf := make([]byte, 512)
		for {
			n, err := conn.Read(buf)
			if err != nil { break }
			// Unmask and parse WebSocket frame
			if n < 6 { continue }
			masked := (buf[1] & 0x80) != 0
			payLen := int(buf[1] & 0x7f)
			off := 2
			if payLen == 126 { off = 4 } // skip extended length handling for small messages
			if !masked || off+4+payLen > n { continue }
			mask := buf[off : off+4]
			payload := make([]byte, payLen)
			for i := 0; i < payLen; i++ {
				payload[i] = buf[off+4+i] ^ mask[i%4]
			}
			// Control message: 0x80 = subscribe FFT, 0x81 = unsubscribe FFT
			if len(payload) >= 2 {
				portIdx := int(payload[1])
				client.mu.Lock()
				if payload[0] == 0x80 {
					client.fftSubs[portIdx] = true
					C.jack_fft_subscribe(C.int(portIdx), 1)
				} else if payload[0] == 0x81 {
					delete(client.fftSubs, portIdx)
					// Only unsubscribe in C if no other client wants it
					wsMu.Lock()
					stillNeeded := false
					for c := range wsConns {
						if c != client {
							c.mu.Lock()
							if c.fftSubs[portIdx] { stillNeeded = true }
							c.mu.Unlock()
						}
					}
					wsMu.Unlock()
					if !stillNeeded { C.jack_fft_subscribe(C.int(portIdx), 0) }
				}
				client.mu.Unlock()
			}
		}
		// Cleanup: unsubscribe all FFTs for this client
		client.mu.Lock()
		subs := make(map[int]bool)
		for k := range client.fftSubs { subs[k] = true }
		client.mu.Unlock()

		wsMu.Lock()
		delete(wsConns, client)
		for portIdx := range subs {
			stillNeeded := false
			for c := range wsConns {
				c.mu.Lock()
				if c.fftSubs[portIdx] { stillNeeded = true }
				c.mu.Unlock()
			}
			if !stillNeeded { C.jack_fft_subscribe(C.int(portIdx), 0) }
		}
		wsMu.Unlock()
		conn.Close()
	}()
}

func wsFrameEncode(payload []byte) []byte {
	n := len(payload)
	if n < 126 {
		frame := make([]byte, 2+n)
		frame[0] = 0x82; frame[1] = byte(n)
		copy(frame[2:], payload); return frame
	}
	frame := make([]byte, 4+n)
	frame[0] = 0x82; frame[1] = 126
	binary.BigEndian.PutUint16(frame[2:4], uint16(n))
	copy(frame[4:], payload); return frame
}

func broadcastWS(data []byte, _ bool) {
	frame := wsFrameEncode(data)
	wsMu.Lock(); defer wsMu.Unlock()
	for c := range wsConns {
		c.conn.SetWriteDeadline(time.Now().Add(50 * time.Millisecond))
		if _, err := c.conn.Write(frame); err != nil {
			c.conn.Close(); delete(wsConns, c)
		}
	}
}
