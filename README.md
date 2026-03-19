# JACK Patchbay

A web-based audio patchbay for [JACK Audio Connection Kit](https://jackaudio.org/) with real-time metering, built as a single Go binary with an embedded web UI.

![Matrix patchbay with VU meters, connection routing, and preset management](https://img.shields.io/badge/status-alpha-blue)

## Features

- **Matrix patchbay** -- click to connect/disconnect any source to any destination
- **Real-time VU meters** -- peak + RMS metering via native JACK API (CGo), not subprocess shelling
- **Binary WebSocket protocol** -- int16 dB values at 30Hz, ~4KB/s for 32 channels
- **Signal-driven animations** -- connection dots glow when audio flows through them
- **Peak hold** with 1s hold time and 1.5dB/frame decay
- **Latency display** per port (read from JACK latency API)
- **Preset system** -- save/recall/delete routing presets (browser localStorage)
- **Mobile responsive** -- stacks vertically on phones/tablets
- **Single binary** -- web UI embedded via `go:embed`, zero runtime dependencies beyond JACK
- **Auto-reconnect** -- SSE and WebSocket reconnect with exponential backoff

## Architecture

```
JACK process callback (C, real-time thread)
  -- reads peak + RMS for all ports every audio buffer
  |
Go server (main thread)
  |-- SSE /api/events     -- JSON state (ports, connections) at 2Hz, only on change
  |-- WS  /api/meters     -- binary peak+RMS at 30Hz (configurable)
  |-- POST /api/connect    -- native jack_connect()
  |-- POST /api/disconnect -- native jack_disconnect()
  |-- GET  /api/state      -- JSON snapshot
  |-- GET  /               -- embedded web UI
  |
Browser
  |-- WebSocket receives binary meter frames, writes to shared buffer
  |-- requestAnimationFrame loop reads buffer, updates DOM (decoupled)
  |-- SSE receives state changes, rebuilds matrix only when structure changes
```

### Binary Meter Protocol

```
Offset  Size   Field
------- ------ ---------------------------------
0       1      Message type (0x01 = meters)
1       1      Channel count (N)
2       4      Timestamp (uint32 LE, milliseconds)
6       N*2    Peak levels (int16 LE, 0.01 dB units)
6+N*2   N*2    RMS levels (int16 LE, 0.01 dB units)
```

For 18 channels: 6 + 18*4 = 78 bytes per frame. At 30fps = ~2.3 KB/s.

## Requirements

- **JACK2** (`jackd2`, `libjack-jackd2-dev`)
- **Go 1.19+** with CGo enabled
- **GCC** (for CGo compilation)
- A running JACK server

## Build

```bash
# On the target machine (needs libjack headers):
CGO_ENABLED=1 go build -o jack-patchbay -ldflags="-s -w" .

# Cross-compile is NOT supported (CGo + libjack)
```

## Install

```bash
sudo cp jack-patchbay /usr/local/bin/
sudo tee /etc/systemd/system/jack-patchbay.service > /dev/null <<EOF
[Unit]
Description=JACK Patchbay Web UI
After=jackd.service
Wants=jackd.service

[Service]
Type=simple
User=jack
Group=audio
Environment=JACK_NO_AUDIO_RESERVATION=1
ExecStart=/usr/local/bin/jack-patchbay -addr :8998
Restart=on-failure
RestartSec=3

[Install]
WantedBy=multi-user.target
EOF

sudo systemctl daemon-reload
sudo systemctl enable --now jack-patchbay
```

Then open `http://<host>:8998` in a browser.

## Usage

```
jack-patchbay [flags]

Flags:
  -addr string       Listen address (default ":8998")
  -meter-hz int      Meter update rate in Hz (default 30)
  -state-hz float    State poll rate in Hz (default 2)
  -jack-user string  Run JACK commands as this user via sudo (optional)
```

### Presets

Type a name in the preset bar and click **Save** to store the current routing. Click a preset name to recall it (disconnects all, then reconnects from the preset). Presets are stored in the browser's localStorage.

## Design Decisions

**Why CGo instead of shelling out to `jack_lsp`/`jack_connect`?**

Shelling out spawns a new process per command, can't read audio buffers, and adds ~5ms latency per call. The native JACK API gives us real-time buffer access for metering, instant port enumeration, and reliable connection management.

**Why WebSocket for meters instead of SSE?**

SSE adds text framing overhead (`event:`, `data:`, newlines) and requires base64 encoding for binary data. WebSocket sends raw binary frames with 2 bytes of overhead. At 30Hz with 18 channels, that's 78 bytes/frame vs ~200 bytes with SSE+base64.

**Why `requestAnimationFrame` instead of rendering in `onmessage`?**

Data arrives at 30Hz from the WebSocket. The browser may render at 60Hz or throttle to lower rates. Decoupling data ingestion from rendering prevents dropped frames and unnecessary reflows. The `onmessage` handler only writes to a shared buffer; the rAF loop reads it.

**Why int16 in 0.01 dB instead of float32?**

A signed int16 covers -327.68 to +327.67 dB with 0.01 dB precision -- more than enough for audio metering (-96 to +24 dB). Saves 2 bytes per channel per value vs float32, and avoids NaN/Inf edge cases on the JavaScript side.

## Theming

The UI uses the same dark theme as the [AES67 Linux Daemon](https://github.com/bondagit/aes67-linux-daemon) WebUI:

- Background: `#06080c` to `#131720`
- Accent: `#4a90e2` (blue)
- Routes: `#00d4aa` (teal)
- Danger: `#e85555` (red)
- Font: Inter + JetBrains Mono

## Integration with AES67 Linux Daemon

This patchbay is designed to work alongside the AES67 daemon's RAVENNA/JACK bridge. The daemon manages AES67 network streams via Netlink, while JACK handles local audio routing. The patchbay provides the visual routing interface.

Typical port layout:

| Port | Direction | Description |
|------|-----------|-------------|
| `ravenna-in:capture_*` | Source | Audio FROM AES67 network |
| `ravenna-out:playback_*` | Destination | Audio TO AES67 network |
| `shairport-sync:out_L/R` | Source | AirPlay audio |
| `<device>-in:capture_*` | Source | Local audio input |
| `<device>-out:playback_*` | Destination | Local audio output |

## License

MIT
