# JACK Patchbay

A web-based audio patchbay for [JACK Audio Connection Kit](https://jackaudio.org/) with real-time metering, per-channel FFT spectrum analyzer, WebRTC audio monitoring, and Opus encoding -- built as a single Go binary with an embedded web UI.

![Matrix patchbay with VU meters, connection routing, and preset management](https://img.shields.io/badge/status-alpha-blue)

<img width="2400" height="1492" alt="image" src="https://github.com/user-attachments/assets/1b29f993-3f3d-452b-9122-cedccb32ad75" />

## Features

### Patchbay
- **Matrix routing** -- click to connect/disconnect any source to any destination
- **Signal-driven animations** -- connection dots glow when audio flows through them
- **Preset system** -- save/recall/delete routing presets (dropdown, localStorage)
- **Dark/light theme** -- toggle with persistence

### Metering
- **Real-time VU meters** -- peak + RMS via native JACK API (CGo), not subprocess shelling
- **Binary WebSocket protocol** -- int16 dB values at 30Hz, ~4KB/s for 32 channels
- **Peak hold** with 1s hold time and 1.5dB/frame decay
- **Latency display** per port (from JACK latency API)

### FFT Spectrum Analyzer
- **Per-channel RTA** -- click the eye icon to open a spectrum display
- **512-point FFT** with Hanning window, 256 bins (~93Hz resolution at 48kHz)
- **Subscription model** -- only computes FFT for ports a client is watching
- **15fps** binary updates, canvas-rendered with frequency labels

### Audio Monitoring (WebRTC)
- **Browser-based monitoring** -- click headphone icon to listen to any port
- **WebRTC + Opus** -- proper jitter buffering, clock recovery, no underruns
- **Mono or stereo** -- select one port for mono, two for stereo pair
- **Bitrate switch** -- 32kbps or 64kbps Opus, changeable on the fly
- **Stats widget** -- live WebRTC RTT, jitter, packet loss, and bitrate display

### General
- **Mobile responsive** -- stacks vertically on phones/tablets
- **Single binary** -- web UI embedded via `go:embed`
- **Auto-reconnect** -- SSE and WebSocket reconnect with exponential backoff
- **UI RTT display** -- ping-based round-trip measurement in header

## Architecture

```
JACK process callback (C, real-time thread)
  |-- peak + RMS metering for all ports
  |-- FFT computation for subscribed ports (512-pt, Hanning window)
  |-- audio capture ring buffer for WebRTC monitor
  |
Go server
  |-- SSE  /api/events          JSON state at 2Hz (only on change)
  |-- WS   /api/meters          binary peak+RMS at 30Hz
  |-- WS   /api/meters          binary FFT at 15Hz (per subscription)
  |-- POST /api/connect          native jack_connect()
  |-- POST /api/disconnect       native jack_disconnect()
  |-- POST /api/monitor/start    WebRTC SDP offer/answer, starts Opus stream
  |-- POST /api/monitor/stop     tears down WebRTC peer connection
  |-- POST /api/monitor/bitrate  change Opus bitrate (32k/64k)
  |-- GET  /api/state            JSON snapshot
  |-- GET  /api/ping             RTT measurement endpoint
  |-- GET  /                     embedded web UI
  |
Browser
  |-- WebSocket: binary meter frames -> shared buffer -> rAF render loop
  |-- WebSocket: FFT subscription control (0x80/0x81) + spectrum canvas
  |-- SSE: state changes -> matrix rebuild only when structure changes
  |-- WebRTC: Opus audio playback via <audio> element
  |-- RTCPeerConnection.getStats(): RTT, jitter, packet loss display
```

### Binary Protocols (multiplexed on single WebSocket)

**VU Meters (type 0x01, 30Hz):**
```
Offset  Size   Field
0       1      Message type (0x01)
1       1      Channel count (N)
2       4      Timestamp (uint32 LE, ms)
6       N*2    Peak levels (int16 LE, 0.01 dB)
6+N*2   N*2    RMS levels (int16 LE, 0.01 dB)
```

**FFT Spectrum (type 0x02, 15Hz, per-subscription):**
```
0       1      Message type (0x02)
1       1      Port index
2       2      Bin count (uint16 LE)
4       4      Timestamp (uint32 LE, ms)
8       N*2    Magnitudes (int16 LE, 0.01 dB)
```

**Control messages (client -> server):**
```
0x80 [portIdx]    Subscribe FFT for port
0x81 [portIdx]    Unsubscribe FFT for port
```

## Requirements

- **JACK2** (`jackd2`, `libjack-jackd2-dev`)
- **libopus** (`libopus-dev`, `libopusfile-dev`) -- for WebRTC Opus encoding
- **Go 1.19+** with CGo enabled
- **GCC** (for CGo compilation)
- A running JACK server

## Build

```bash
# Install dependencies (Debian/Ubuntu/Raspbian):
sudo apt-get install -y jackd2 libjack-jackd2-dev libopus-dev libopusfile-dev

# Build (on the target machine -- CGo requires native headers):
CGO_ENABLED=1 go build -o jack-patchbay -ldflags="-s -w" .
```

Pre-built binaries for linux/amd64, linux/arm64, and linux/armv7 are available on the [releases page](https://github.com/DatanoiseTV/jack-patchbay/releases).

> **Note:** Pre-built binaries require `libjack`, `libopus`, and `libopusfile` to be installed on the target system.

## Install

```bash
# From source:
make install

# Or manually:
sudo cp jack-patchbay /usr/local/bin/
```

### Systemd service

```bash
make install-service
# or see Makefile for the service file template
sudo systemctl start jack-patchbay
```

Then open `http://<host>:8998` in a browser.

## Usage

```
jack-patchbay [flags]

Flags:
  -addr string       Listen address (default ":8998")
  -meter-hz int      Meter update rate in Hz (default 30)
  -fft-hz int        FFT update rate in Hz (default 15)
  -state-hz float    State poll rate in Hz (default 2)
```

### Patchbay
Click any intersection in the matrix to connect/disconnect ports.

### Presets
Select from the dropdown or type a name and click **Save**. Click **x** to delete.

### FFT Spectrum
Click the **eye** icon next to any port to open its real-time spectrum analyzer. Click again to close (stops FFT computation server-side).

### Audio Monitoring
Click the **headphone** icon on one port for mono, or two ports for stereo. A floating widget shows WebRTC stats (RTT, jitter, packet loss, bitrate). Toggle between 32k and 64k Opus. Click **Stop** to end.

### Theme
Click the sun/moon icon in the header to toggle dark/light mode (persisted in localStorage).

## Performance

| Feature | Bandwidth | CPU (RPi5) |
|---------|-----------|------------|
| VU meters (18ch, 30Hz) | ~2.3 KB/s | negligible |
| FFT (1 port, 15Hz) | ~7.7 KB/s | ~1% per port |
| WebRTC monitor (stereo, 64k Opus) | ~8 KB/s | ~2% |
| State SSE (2Hz) | ~0.5 KB/s | negligible |

Recommended: pin audio services to isolated CPU cores for lowest latency:
```bash
# In /boot/cmdline.txt (or /boot/firmware/cmdline.txt):
isolcpus=2,3 nohz_full=2,3 rcu_nocbs=2,3

# Systemd service drop-in:
# /etc/systemd/system/jackd.service.d/cpuaffinity.conf
[Service]
CPUAffinity=2 3
```

## Design Decisions

**Why CGo instead of shelling out to `jack_lsp`/`jack_connect`?**
Shelling out spawns a new process per command, can't read audio buffers, and adds ~5ms latency per call. The native JACK API gives us real-time buffer access for metering, instant port enumeration, and reliable connection management.

**Why WebSocket for meters instead of SSE?**
SSE adds text framing overhead and requires base64 for binary data. WebSocket sends raw binary frames with 2 bytes of overhead.

**Why `requestAnimationFrame` instead of rendering in `onmessage`?**
Decoupling data ingestion from rendering prevents dropped frames and unnecessary reflows.

**Why int16 in 0.01 dB instead of float32?**
Covers -327.68 to +327.67 dB with 0.01 dB precision. Saves 2 bytes per value, avoids NaN/Inf edge cases in JS.

**Why WebRTC for audio monitoring instead of WebSocket PCM?**
WebRTC provides built-in jitter buffering, clock recovery, and Opus encoding. Raw PCM over WebSocket causes underruns due to clock drift and network jitter.

**Why subscription model for FFT?**
512-point FFT per port at 15fps is ~1% CPU on an RPi5. With 20 ports, computing all FFTs wastes 20% CPU. Subscription ensures we only compute what's being viewed.

## Integration with AES67 Linux Daemon

This patchbay is designed to work alongside the [AES67 Linux Daemon](https://github.com/bondagit/aes67-linux-daemon) RAVENNA/JACK bridge:

| Port | Direction | Description |
|------|-----------|-------------|
| `ravenna-in:capture_*` | Source | Audio FROM AES67 network |
| `ravenna-out:playback_*` | Destination | Audio TO AES67 network |
| `shairport-sync:out_L/R` | Source | AirPlay audio |
| `<device>-in:capture_*` | Source | Local audio input (USB, I2S) |
| `<device>-out:playback_*` | Destination | Local audio output |

## License

MIT
