package main

/*
#cgo LDFLAGS: -ljack -lm
#include <jack/jack.h>
#include <stdlib.h>
#include <string.h>
#include <math.h>

#define MAX_PORTS 256

typedef struct {
    jack_client_t *client;
    char port_names[MAX_PORTS][256];
    float peaks[MAX_PORTS];
    float rms_acc[MAX_PORTS];   // running sum of squares
    float rms[MAX_PORTS];       // computed RMS
    int   rms_count[MAX_PORTS]; // sample count for RMS window
    int   port_count;
    int   active;
} meter_state_t;

static meter_state_t g_meter;

static int process_callback(jack_nframes_t nframes, void *arg) {
    meter_state_t *m = (meter_state_t *)arg;
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
        }

        // Peak: instant attack, smooth decay
        if (peak > m->peaks[i]) m->peaks[i] = peak;
        else m->peaks[i] = m->peaks[i] * 0.85f + peak * 0.15f;

        // RMS: accumulate over ~300ms window then reset
        m->rms_acc[i] += sum_sq;
        m->rms_count[i] += nframes;
        // ~300ms window at 48kHz = 14400 samples
        if (m->rms_count[i] >= 14400) {
            m->rms[i] = sqrtf(m->rms_acc[i] / (float)m->rms_count[i]);
            m->rms_acc[i] = 0.0f;
            m->rms_count[i] = 0;
        }
    }
    return 0;
}

static void on_jack_shutdown(void *arg) {
    meter_state_t *m = (meter_state_t *)arg;
    m->active = 0;
    m->client = NULL;
    m->port_count = 0;
}

static int jack_init() {
    if (g_meter.client) { jack_client_close(g_meter.client); g_meter.client = NULL; }
    g_meter.active = 0; g_meter.port_count = 0;
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
    // Zero new entries
    for (int i = g_meter.port_count; i < count; i++) {
        g_meter.peaks[i] = 0; g_meter.rms[i] = 0;
        g_meter.rms_acc[i] = 0; g_meter.rms_count[i] = 0;
    }
    g_meter.port_count = count;
    for (int i = 0; i < count; i++) {
        strncpy(g_meter.port_names[i], names[i], 255);
        g_meter.port_names[i][255] = '\0';
    }
}

// Read all peaks+rms in one call
typedef struct { float peak; float rms; } meter_reading_t;

static int jack_read_meters(meter_reading_t *out, int max) {
    int n = g_meter.port_count;
    if (n > max) n = max;
    for (int i = 0; i < n; i++) {
        out[i].peak = g_meter.peaks[i];
        out[i].rms  = g_meter.rms[i];
    }
    return n;
}

static jack_nframes_t meter_get_sample_rate() {
    if (!g_meter.client) return 48000;
    return jack_get_sample_rate(g_meter.client);
}

static jack_nframes_t meter_get_buffer_size() {
    if (!g_meter.client) return 48;
    return jack_get_buffer_size(g_meter.client);
}

typedef struct {
    char name[256];
    int  flags;
    jack_nframes_t latency;
} port_info_t;

static int jack_list_ports(port_info_t *out, int max) {
    if (!g_meter.client) return 0;
    const char **ports = jack_get_ports(g_meter.client, NULL, NULL, 0);
    if (!ports) return 0;
    int n = 0;
    for (int i = 0; ports[i] && n < max; i++) {
        strncpy(out[n].name, ports[i], 255);
        out[n].name[255] = '\0';
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
    for (int i = 0; conns[i] && n < max; i++) {
        strncpy(out[n], conns[i], 255); out[n][255] = '\0'; n++;
    }
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

type Connection struct {
	Source string `json:"source"`
	Dest   string `json:"dest"`
}

type State struct {
	Ports       []Port       `json:"ports"`
	Connections []Connection `json:"connections"`
	Clients     []string     `json:"clients"`
	SampleRate  int          `json:"sampleRate"`
	BufferSize  int          `json:"bufferSize"`
}

var (
	stateMu   sync.RWMutex
	lastState *State
	sseMu     sync.Mutex
	sseChans  = make(map[chan []byte]struct{})
	wsMu      sync.Mutex
	wsConns   = make(map[net.Conn]struct{})
)

func main() {
	addr := flag.String("addr", ":8998", "Listen address")
	meterHz := flag.Int("meter-hz", 30, "Meter update rate")
	stateHz := flag.Float64("state-hz", 2, "State poll rate")
	flag.Parse()

	log.Printf("JACK Patchbay on %s (meters %dHz, state %.0fHz)", *addr, *meterHz, *stateHz)
	go jackConnectLoop()
	go statePollLoop(time.Duration(float64(time.Second) / *stateHz))
	go meterLoop(time.Duration(float64(time.Second) / float64(*meterHz)))

	sub, _ := fs.Sub(staticFS, "static")
	mux := http.NewServeMux()
	mux.HandleFunc("/api/state", apiState)
	mux.HandleFunc("/api/connect", apiConnect)
	mux.HandleFunc("/api/disconnect", apiDisconnect)
	mux.HandleFunc("/api/events", apiSSE)
	mux.HandleFunc("/api/meters", apiMetersWS)
	mux.HandleFunc("/api/ping", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	mux.Handle("/", http.FileServer(http.FS(sub)))
	log.Fatal(http.ListenAndServe(*addr, mux))
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
		if C.jack_is_active() == 0 {
			continue
		}
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

// linToDb converts linear amplitude to dB, clamped to [-96, +24]
func linToDb(lin float64) float64 {
	if lin < 1e-5 {
		return -96.0
	}
	db := 20.0 * math.Log10(lin)
	if db < -96.0 {
		return -96.0
	}
	if db > 24.0 {
		return 24.0
	}
	return db
}

// dbToInt16 converts dB to int16 in 0.01 dB units
func dbToInt16(db float64) int16 {
	v := db * 100.0
	if v < -32768 {
		return -32768
	}
	if v > 32767 {
		return 32767
	}
	return int16(v)
}

// meterLoop: read peaks+RMS, pack as binary, broadcast via WebSocket
// Protocol: [type:u8] [count:u8] [timestamp:u32le] [peak0:i16le]...[peakN:i16le] [rms0:i16le]...[rmsN:i16le]
func meterLoop(interval time.Duration) {
	var readings [256]C.meter_reading_t
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for range ticker.C {
		if C.jack_is_active() == 0 {
			continue
		}
		stateMu.RLock()
		st := lastState
		stateMu.RUnlock()
		if st == nil || len(st.Ports) == 0 {
			continue
		}

		n := int(C.jack_read_meters(&readings[0], 256))
		if n > len(st.Ports) {
			n = len(st.Ports)
		}

		// Pack binary frame: 1+1+4 + n*2 + n*2 bytes
		frameSize := 6 + n*4
		buf := make([]byte, frameSize)
		buf[0] = 0x01 // message type: meters
		buf[1] = byte(n)
		binary.LittleEndian.PutUint32(buf[2:], uint32(time.Now().UnixMilli()))

		off := 6
		for i := 0; i < n; i++ {
			peakDb := linToDb(float64(readings[i].peak))
			binary.LittleEndian.PutUint16(buf[off:], uint16(dbToInt16(peakDb)))
			off += 2
		}
		for i := 0; i < n; i++ {
			rmsDb := linToDb(float64(readings[i].rms))
			binary.LittleEndian.PutUint16(buf[off:], uint16(dbToInt16(rmsDb)))
			off += 2
		}

		broadcastWS(buf)
	}
}

func readState() *State {
	var infos [256]C.port_info_t
	n := int(C.jack_list_ports(&infos[0], 256))
	st := &State{}
	clientSet := map[string]bool{}
	sampleRate := float64(C.meter_get_sample_rate())

	for i := 0; i < n; i++ {
		name := C.GoString(&infos[i].name[0])
		parts := strings.SplitN(name, ":", 2)
		if len(parts) != 2 || strings.Contains(parts[0], " ") {
			continue
		}
		isOut := (infos[i].flags & C.JackPortIsOutput) != 0
		latMs := float64(infos[i].latency) / sampleRate * 1000.0

		var conns [64][256]C.char
		cName := C.CString(name)
		nConns := int(C.jack_port_connections(cName, &conns[0], 64))
		C.free(unsafe.Pointer(cName))
		for j := 0; j < nConns; j++ {
			dest := C.GoString(&conns[j][0])
			if isOut {
				st.Connections = append(st.Connections, Connection{Source: name, Dest: dest})
			}
		}

		clientSet[parts[0]] = true
		st.Ports = append(st.Ports, Port{
			Name: name, Client: parts[0], Port: parts[1],
			IsOut: isOut, Connections: nConns,
			LatencyMs: math.Round(latMs*10) / 10,
		})
	}

	sort.Slice(st.Ports, func(i, j int) bool {
		if st.Ports[i].Client != st.Ports[j].Client {
			return st.Ports[i].Client < st.Ports[j].Client
		}
		return naturalLess(st.Ports[i].Port, st.Ports[j].Port)
	})
	cNames := make([]*C.char, len(st.Ports))
	for i, p := range st.Ports {
		cNames[i] = C.CString(p.Name)
	}
	if len(cNames) > 0 {
		C.jack_update_ports(&cNames[0], C.int(len(cNames)))
	}
	for _, c := range cNames {
		C.free(unsafe.Pointer(c))
	}
	for c := range clientSet {
		st.Clients = append(st.Clients, c)
	}
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
			for ai < len(a) && a[ai] >= '0' && a[ai] <= '9' {
				na = na*10 + int(a[ai]-'0')
				ai++
			}
			for bi < len(b) && b[bi] >= '0' && b[bi] <= '9' {
				nb = nb*10 + int(b[bi]-'0')
				bi++
			}
			if na != nb {
				return na < nb
			}
			continue
		}
		if ca != cb {
			return ca < cb
		}
		ai++
		bi++
	}
	return len(a) < len(b)
}

// --- HTTP ---

func apiState(w http.ResponseWriter, r *http.Request) {
	stateMu.RLock()
	s := lastState
	stateMu.RUnlock()
	if s == nil {
		http.Error(w, "not ready", 503)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s)
}

func apiConnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "POST", 405)
		return
	}
	var c Connection
	json.NewDecoder(r.Body).Decode(&c)
	src, dst := C.CString(c.Source), C.CString(c.Dest)
	rc := C.jack_do_connect(src, dst)
	C.free(unsafe.Pointer(src))
	C.free(unsafe.Pointer(dst))
	if rc != 0 {
		http.Error(w, fmt.Sprintf("connect failed: %d", rc), 500)
		return
	}
	w.WriteHeader(200)
}

func apiDisconnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "POST", 405)
		return
	}
	var c Connection
	json.NewDecoder(r.Body).Decode(&c)
	src, dst := C.CString(c.Source), C.CString(c.Dest)
	rc := C.jack_do_disconnect(src, dst)
	C.free(unsafe.Pointer(src))
	C.free(unsafe.Pointer(dst))
	if rc != 0 {
		http.Error(w, fmt.Sprintf("disconnect failed: %d", rc), 500)
		return
	}
	w.WriteHeader(200)
}

// --- SSE ---

func apiSSE(w http.ResponseWriter, r *http.Request) {
	f, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "no streaming", 500)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	ch := make(chan []byte, 3)
	sseMu.Lock()
	sseChans[ch] = struct{}{}
	sseMu.Unlock()
	defer func() { sseMu.Lock(); delete(sseChans, ch); sseMu.Unlock() }()

	stateMu.RLock()
	if lastState != nil {
		d, _ := json.Marshal(lastState)
		fmt.Fprintf(w, "data: %s\n\n", d)
		f.Flush()
	}
	stateMu.RUnlock()

	for {
		select {
		case d := <-ch:
			fmt.Fprintf(w, "data: %s\n\n", d)
			f.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

func broadcastSSE(data []byte) {
	sseMu.Lock()
	defer sseMu.Unlock()
	for ch := range sseChans {
		select {
		case ch <- data:
		default:
		}
	}
}

// --- WebSocket (minimal, no deps) ---

func apiMetersWS(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Upgrade") != "websocket" {
		http.Error(w, "websocket required", 400)
		return
	}
	key := r.Header.Get("Sec-WebSocket-Key")
	if key == "" {
		http.Error(w, "missing key", 400)
		return
	}
	h := sha1.New()
	h.Write([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	accept := base64.StdEncoding.EncodeToString(h.Sum(nil))

	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "no hijack", 500)
		return
	}
	conn, bufrw, err := hj.Hijack()
	if err != nil {
		return
	}
	bufrw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " + accept + "\r\n\r\n")
	bufrw.Flush()

	wsMu.Lock()
	wsConns[conn] = struct{}{}
	wsMu.Unlock()

	go func() {
		buf := make([]byte, 256)
		for {
			if _, err := conn.Read(buf); err != nil {
				break
			}
		}
		wsMu.Lock()
		delete(wsConns, conn)
		wsMu.Unlock()
		conn.Close()
	}()
}

func wsFrame(payload []byte) []byte {
	n := len(payload)
	var frame []byte
	if n < 126 {
		frame = make([]byte, 2+n)
		frame[0] = 0x82
		frame[1] = byte(n)
		copy(frame[2:], payload)
	} else {
		frame = make([]byte, 4+n)
		frame[0] = 0x82
		frame[1] = 126
		binary.BigEndian.PutUint16(frame[2:4], uint16(n))
		copy(frame[4:], payload)
	}
	return frame
}

func broadcastWS(data []byte) {
	frame := wsFrame(data)
	wsMu.Lock()
	defer wsMu.Unlock()
	for conn := range wsConns {
		conn.SetWriteDeadline(time.Now().Add(50 * time.Millisecond))
		if _, err := conn.Write(frame); err != nil {
			conn.Close()
			delete(wsConns, conn)
		}
	}
}
