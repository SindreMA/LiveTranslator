# LiveTranslator — Implementation Plan

## Overview

A Windows system-tray application written in Go that captures system audio (WASAPI loopback), streams chunks to a self-hosted Whisper API for transcription/translation, and renders subtitles in a transparent always-on-top overlay at the bottom-center of the screen.

---

## Architecture

```
┌──────────────┐      WAV chunks       ┌────────────────┐
│ Audio Capture │ ───────────────────►  │  Whisper Client │
│ (WASAPI loop) │  (on voice boundary)  │  (HTTP POST)    │
└──────────────┘                        └───────┬────────┘
                                                │ {"text":"..."}
                                                ▼
┌──────────────┐                        ┌────────────────┐
│  System Tray  │ ◄──── state ────────► │  Overlay Window │
│  (Win32 API)  │                       │  (Win32 layered)│
└──────────────┘                        └────────────────┘
        │
        ▼
┌──────────────┐
│   Settings    │
│   Window      │
└──────────────┘
```

---

## Modules

### 1. Audio Capture (`internal/audio`)

- **WASAPI loopback capture** via Windows COM APIs (golang.org/x/sys/windows + manual COM calls, or cgo wrapper around a small C shim).
- Enumerate audio render (output) devices so the user can pick one in settings. Default to the system default output device.
- Capture runs in its own goroutine, writing PCM samples into a ring buffer.
- A **chunker** goroutine reads from the ring buffer and produces WAV-encoded chunks, sent over a channel.

**Chunk strategy — voice-activity boundary detection:**
- Accumulate audio up to a **max window** (e.g. 4 seconds).
- After a **min window** (e.g. 1.5 seconds), start watching for a silence gap (amplitude below threshold for ≥ 300 ms).
- When silence is detected (or max window is hit), flush the chunk. This avoids cutting mid-word.
- Overlap: prepend ~200 ms of the previous chunk's tail to smooth word boundaries.

### 2. Whisper Client (`internal/whisper`)

- HTTP client that POSTs `multipart/form-data` to the Whisper API.
- Endpoint: `{base_url}/v1/audio/transcriptions`
- Fields: `file` (WAV bytes), `language` (optional), `response_format=json`.
- For **translation mode**: add `task=translate` form field (requires a small server-side change — see below).
- Runs requests concurrently but delivers results **in order** so subtitles don't appear out of sequence. Simple approach: sequential send with a timeout.
- Returns `string` results over a channel to the overlay.

**Server-side change needed:** Add `task: str = Form(default="transcribe")` parameter to `server.py` and pass it to `_model.transcribe(tmp_path, language=..., task=task)`. Whisper natively supports `task="translate"` (translates to English).

### 3. Overlay Window (`internal/overlay`)

- **Win32 layered window** (WS_EX_LAYERED | WS_EX_TOPMOST | WS_EX_TRANSPARENT | WS_EX_TOOLWINDOW).
- Click-through so it doesn't steal focus.
- Positioned at bottom-center of the primary monitor, ~10% from the bottom edge.
- Renders **1–2 lines** of text with a semi-transparent dark background pill behind the text, white text, configurable font size (default ~22pt).
- When new text arrives:
  - If it fits on one line, show one line.
  - If it wraps, show up to two lines; if it exceeds two lines, scroll (drop the oldest line).
- Text fades out after ~5 seconds of no new input.
- Uses GDI+ or DirectWrite for anti-aliased text rendering.

### 4. System Tray (`internal/tray`)

- Win32 Shell_NotifyIcon API for the tray icon.
- Right-click context menu:
  - **Start / Stop** listening
  - **Transcribe / Translate** toggle (shows current mode with a checkmark)
  - **Settings…** (opens settings window)
  - **Quit**
- Left-click: toggle overlay on/off (same as hotkey).
- Tray icon tooltip shows current state (e.g. "LiveTranslator — Transcribing").

### 5. Settings Window (`internal/settings`)

- A simple Win32 dialog or Walk-based window with:
  - **Audio device** dropdown (enumerate WASAPI render endpoints)
  - **Whisper API URL** text field (default: `http://localhost:8000`)
  - **Language** dropdown (common languages + "Auto-detect")
  - **Mode** radio: Transcribe / Translate
  - **Overlay font size** slider (16–36pt)
  - **Overlay opacity** slider
  - **Hotkey** key-combo picker (default: `Ctrl+Shift+T`)
  - **Save / Cancel** buttons
- Settings persisted to `%APPDATA%/LiveTranslator/config.json`.

### 6. Hotkey (`internal/hotkey`)

- `RegisterHotKey` Win32 API to register a global hotkey.
- Toggles overlay visibility.
- Hotkey is configurable in settings; re-registered on change.

### 7. Config (`internal/config`)

```go
type Config struct {
    AudioDeviceID   string `json:"audio_device_id"`   // empty = default
    WhisperURL      string `json:"whisper_url"`        // default "http://localhost:8000"
    Language        string `json:"language"`            // "" = auto-detect
    Mode            string `json:"mode"`                // "transcribe" | "translate"
    FontSize        int    `json:"font_size"`           // default 22
    OverlayOpacity  int    `json:"overlay_opacity"`     // 0-255, default 200
    Hotkey          string `json:"hotkey"`              // default "Ctrl+Shift+T"
}
```

---

## Dependencies

| Dependency | Purpose |
|---|---|
| `golang.org/x/sys/windows` | Win32 / COM syscalls |
| `github.com/go-ole/go-ole` | COM/OLE helpers for WASAPI |
| Standard library (`net/http`, `mime/multipart`, `encoding/json`, `os`, `sync`) | HTTP client, config, concurrency |

Minimal external dependencies. No heavy GUI frameworks — raw Win32 for overlay and tray, keeping the binary small and fast.

---

## Project Structure

```
LiveTranslator/
├── cmd/
│   └── livetranslator/
│       └── main.go              # entry point, wires everything together
├── internal/
│   ├── audio/
│   │   ├── devices.go           # enumerate WASAPI render endpoints
│   │   ├── capture.go           # loopback capture goroutine
│   │   └── chunker.go           # silence-detection chunker
│   ├── whisper/
│   │   └── client.go            # HTTP client for whisper API
│   ├── overlay/
│   │   └── overlay.go           # Win32 layered window, text rendering
│   ├── tray/
│   │   └── tray.go              # system tray icon + menu
│   ├── settings/
│   │   └── settings.go          # settings dialog
│   ├── hotkey/
│   │   └── hotkey.go            # global hotkey registration
│   └── config/
│       └── config.go            # load/save JSON config
├── assets/
│   └── icon.ico                 # tray icon
├── plan.md
├── README.md
└── go.mod
```

---

## Implementation Order

### Phase 1 — Skeleton & Audio Capture
1. `go mod init`, project structure, config load/save
2. WASAPI loopback capture with device enumeration
3. Silence-detection chunker producing WAV buffers
4. Verify: capture audio, write chunks to disk, play them back

### Phase 2 — Whisper Integration
5. HTTP client sending chunks to whisper API
6. Server-side: add `task` parameter to `server.py`
7. Verify: capture → send → print transcription to stdout

### Phase 3 — Overlay
8. Win32 layered window with click-through
9. Text rendering (1–2 lines, fade-out)
10. Wire audio→whisper→overlay pipeline end-to-end

### Phase 4 — Tray & Settings
11. System tray icon with context menu
12. Settings window (device picker, API URL, mode toggle, etc.)
13. Global hotkey registration

### Phase 5 — Polish
14. Graceful shutdown, error handling, reconnection
15. App icon, build script (`go build -ldflags -H=windowsgui`)
16. Testing on different audio configurations

---

## Open Decisions

- **Walk vs raw Win32 for settings window:** The overlay and tray will use raw Win32 for maximum control. The settings window could use Walk for easier form layout without adding much overhead. Recommendation: **Walk for settings only, raw Win32 for everything else.**
- **CGo for WASAPI vs pure Go COM:** Pure Go via `go-ole` is preferred to avoid CGo complexity, but WASAPI COM interfaces are verbose. Will prototype and fall back to a thin C shim if needed.
