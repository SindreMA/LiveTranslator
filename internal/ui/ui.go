package ui

import (
	"encoding/json"
	"fmt"
	"log"
	"runtime"
	"sync"
	"unsafe"

	ole "github.com/go-ole/go-ole"
	"golang.org/x/sys/windows"

	"github.com/SindreMA/LiveTranslator/internal/audio"
	"github.com/SindreMA/LiveTranslator/internal/config"
	"github.com/jchv/go-webview2"
)

var (
	user32dll                    = windows.NewLazySystemDLL("user32.dll")
	dwmdll                       = windows.NewLazySystemDLL("dwmapi.dll")
	procGetWindowLongPtrW        = user32dll.NewProc("GetWindowLongPtrW")
	procSetWindowLongPtrW        = user32dll.NewProc("SetWindowLongPtrW")
	procSetWindowPosUI           = user32dll.NewProc("SetWindowPos")
	procDwmSetAttr               = dwmdll.NewProc("DwmSetWindowAttribute")
	procShowWindowUI             = user32dll.NewProc("ShowWindow")
	procSendMessageUI            = user32dll.NewProc("SendMessageW")
	procReleaseCapture           = user32dll.NewProc("ReleaseCapture")
	procCreateIconFromResourceEx = user32dll.NewProc("CreateIconFromResourceEx")
	procDestroyIcon              = user32dll.NewProc("DestroyIcon")
	procSetForegroundWindowUI    = user32dll.NewProc("SetForegroundWindow")
)

const (
	gwlStyle       = uintptr(0xFFFFFFFFFFFFFFFF - 16 + 1) // -16 as uintptr
	wsCaptionBit   = 0x00C00000
	wsThickframe   = 0x00040000
	wsMinimizebox  = 0x00020000
	wsMaximizebox  = 0x00010000
	wsSysmenu      = 0x00080000
	swpFrameChanged = 0x0020
	swpNoMove       = 0x0002
	swpNoSize       = 0x0001
	swpNoZOrder     = 0x0004
	dwmUseDarkMode  = 20
	wmSetIcon       = 0x0080
	iconBig         = 1
	iconSmall       = 0
	lrDefaultColor  = 0x0000
)

type Callbacks struct {
	OnSave          func(cfg *config.Config)
	OnStartCapture  func()
	OnStopCapture   func()
	OnToggleOverlay func()
	IsCapturing     func() bool
}

type Window struct {
	mu       sync.Mutex
	cb       Callbacks
	cfg      *config.Config
	open     bool
	iconData []byte
	messages []string
}

const maxMessages = 50

func New(cb Callbacks) *Window {
	return &Window{cb: cb}
}

// SetIconData sets the ICO data used for the window's taskbar icon.
func (w *Window) SetIconData(data []byte) {
	w.iconData = data
}

// AddMessage appends a transcription message visible in the UI.
func (w *Window) AddMessage(text string) {
	w.mu.Lock()
	w.messages = append(w.messages, text)
	if len(w.messages) > maxMessages {
		w.messages = w.messages[len(w.messages)-maxMessages:]
	}
	w.mu.Unlock()
}

func (w *Window) Show(cfg *config.Config) {
	w.mu.Lock()
	if w.open {
		w.mu.Unlock()
		return
	}
	w.open = true
	w.cfg = cfg
	w.mu.Unlock()

	go w.createWindow()
}

func (w *Window) createWindow() {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	// WebView2 requires COM STA on the calling thread.
	ole.CoInitializeEx(0, ole.COINIT_APARTMENTTHREADED)
	defer ole.CoUninitialize()

	defer func() {
		w.mu.Lock()
		w.open = false
		w.mu.Unlock()
	}()

	wv := webview2.NewWithOptions(webview2.WebViewOptions{
		Debug:     false,
		AutoFocus: true,
		WindowOptions: webview2.WindowOptions{
			Title:  "LiveTranslator",
			Width:  560,
			Height: 680,
			Center: true,
		},
	})
	if wv == nil {
		log.Println("ui: failed to create webview (WebView2 runtime may not be installed)")
		return
	}
	defer wv.Destroy()

	// Remove native title bar — make frameless so our HTML title bar is the only one.
	hwnd := uintptr(wv.Window())
	if hwnd != 0 {
		// Enable dark mode on the window (in case native bar flashes briefly).
		val := uint32(1)
		procDwmSetAttr.Call(hwnd, dwmUseDarkMode, uintptr(unsafe.Pointer(&val)), 4)

		// Remove caption (title bar + border), keep thick frame for resize.
		style, _, _ := procGetWindowLongPtrW.Call(hwnd, gwlStyle)
		style &^= wsCaptionBit
		style |= wsThickframe
		procSetWindowLongPtrW.Call(hwnd, gwlStyle, style)

		// Force the frame change to take effect.
		procSetWindowPosUI.Call(hwnd, 0, 0, 0, 0, 0,
			swpFrameChanged|swpNoMove|swpNoSize|swpNoZOrder)

		// Set taskbar icon from ICO data.
		if len(w.iconData) > 22 {
			bmpData := w.iconData[22:]
			hIcon, _, _ := procCreateIconFromResourceEx.Call(
				uintptr(unsafe.Pointer(&bmpData[0])),
				uintptr(len(bmpData)),
				1, 0x00030000, 32, 32, lrDefaultColor,
			)
			if hIcon != 0 {
				procSendMessageUI.Call(hwnd, wmSetIcon, iconBig, hIcon)
				procSendMessageUI.Call(hwnd, wmSetIcon, iconSmall, hIcon)
			}
		}

		// Bring window to front.
		procSetForegroundWindowUI.Call(hwnd)
	}

	// Bind Go functions callable from JS.
	wv.Bind("isCapturing", func() bool {
		if w.cb.IsCapturing != nil {
			return w.cb.IsCapturing()
		}
		return false
	})

	wv.Bind("getConfig", func() string {
		w.mu.Lock()
		defer w.mu.Unlock()
		data, _ := json.Marshal(w.cfg)
		return string(data)
	})

	wv.Bind("getDevices", func() string {
		devs, err := audio.ListRenderDevices()
		if err != nil {
			return "[]"
		}
		data, _ := json.Marshal(devs)
		return string(data)
	})

	wv.Bind("saveConfig", func(jsonStr string) string {
		var cfg config.Config
		if err := json.Unmarshal([]byte(jsonStr), &cfg); err != nil {
			return fmt.Sprintf(`{"error":"%s"}`, err.Error())
		}
		if err := cfg.Save(); err != nil {
			return fmt.Sprintf(`{"error":"%s"}`, err.Error())
		}
		if err := cfg.ApplyStartWithWindows(); err != nil {
			log.Printf("ui: failed to update startup registry: %v", err)
		}
		w.mu.Lock()
		*w.cfg = cfg
		w.mu.Unlock()
		if w.cb.OnSave != nil {
			w.cb.OnSave(&cfg)
		}
		return `{"ok":true}`
	})

	wv.Bind("applyConfig", func(jsonStr string) string {
		var cfg config.Config
		if err := json.Unmarshal([]byte(jsonStr), &cfg); err != nil {
			return fmt.Sprintf(`{"error":"%s"}`, err.Error())
		}
		w.mu.Lock()
		*w.cfg = cfg
		w.mu.Unlock()
		if w.cb.OnSave != nil {
			w.cb.OnSave(&cfg)
		}
		return `{"ok":true}`
	})

	wv.Bind("startCapture", func() {
		if w.cb.OnStartCapture != nil {
			w.cb.OnStartCapture()
		}
	})

	wv.Bind("stopCapture", func() {
		if w.cb.OnStopCapture != nil {
			w.cb.OnStopCapture()
		}
	})

	wv.Bind("toggleOverlay", func() {
		if w.cb.OnToggleOverlay != nil {
			w.cb.OnToggleOverlay()
		}
	})

	wv.Bind("getMessages", func() string {
		w.mu.Lock()
		defer w.mu.Unlock()
		data, _ := json.Marshal(w.messages)
		return string(data)
	})

	wv.Bind("closeWindow", func() {
		// Post WM_CLOSE to the window — this triggers the webview's own cleanup.
		if hwnd != 0 {
			procSendMessageUI.Call(hwnd, 0x0010, 0, 0) // WM_CLOSE
		}
	})

	wv.Bind("minimizeWindow", func() {
		if hwnd != 0 {
			procShowWindowUI.Call(hwnd, 6) // SW_MINIMIZE
		}
	})

	wv.Bind("dragWindow", func() {
		if hwnd != 0 {
			// Release capture and send WM_NCLBUTTONDOWN with HTCAPTION to start drag.
			procReleaseCapture.Call()
			procSendMessageUI.Call(hwnd, 0x00A1, 2, 0) // WM_NCLBUTTONDOWN, HTCAPTION
		}
	})

	wv.SetHtml(htmlContent)
	wv.Run()
}

const htmlContent = `<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
<style>
  * { margin: 0; padding: 0; box-sizing: border-box; }
  html, body { height: 100%; overflow: hidden; }
  body {
    font-family: 'Segoe UI', sans-serif;
    background: #1a1a2e;
    color: #e0e0e0;
    user-select: none;
    display: flex;
    flex-direction: column;
  }

  /* Custom title bar */
  .titlebar {
    display: flex;
    align-items: center;
    justify-content: space-between;
    height: 38px;
    background: #12122a;
    padding: 0 8px 0 16px;
    cursor: default;
    flex-shrink: 0;
  }
  .titlebar-title {
    font-size: 13px;
    font-weight: 600;
    color: #aaa;
    letter-spacing: 0.5px;
  }
  .titlebar-drag {
    flex: 1;
    height: 100%;
    cursor: grab;
  }
  .titlebar-drag:active { cursor: grabbing; }
  .titlebar-buttons {
    display: flex;
  }
  .titlebar-btn {
    width: 38px;
    height: 38px;
    display: flex;
    align-items: center;
    justify-content: center;
    background: none;
    border: none;
    color: #888;
    font-size: 16px;
    cursor: pointer;
    transition: all 0.15s;
    border-radius: 4px;
  }
  .titlebar-btn:hover { background: #2a2a4a; color: #ccc; }
  .titlebar-btn.close:hover { background: #c0392b; color: #fff; }

  /* Tabs */
  .tabs {
    display: flex;
    background: #16162a;
    border-bottom: 2px solid #2a2a4a;
    flex-shrink: 0;
  }
  .tab {
    flex: 1;
    padding: 14px 0;
    text-align: center;
    cursor: pointer;
    font-size: 14px;
    font-weight: 600;
    color: #888;
    transition: all 0.2s;
    letter-spacing: 0.5px;
  }
  .tab:hover { color: #bbb; background: #1e1e36; }
  .tab.active {
    color: #fff;
    border-bottom: 2px solid #6c63ff;
    margin-bottom: -2px;
  }

  /* Panels */
  .panel {
    display: none;
    padding: 24px 28px;
    flex: 1;
    overflow-y: auto;
  }
  .panel.active { display: block; }
  .panel::-webkit-scrollbar { width: 6px; }
  .panel::-webkit-scrollbar-track { background: transparent; }
  .panel::-webkit-scrollbar-thumb { background: #3a3a5a; border-radius: 3px; }

  h2 { font-size: 13px; color: #6c63ff; letter-spacing: 1px; margin-bottom: 8px; margin-top: 20px; }
  h2:first-child { margin-top: 0; }

  .row { display: flex; gap: 12px; margin-bottom: 4px; }
  .field { flex: 1; margin-bottom: 12px; }
  .field label {
    display: block;
    font-size: 11px;
    color: #888;
    margin-bottom: 5px;
    text-transform: uppercase;
    letter-spacing: 0.5px;
  }
  .field input, .field select {
    width: 100%;
    padding: 9px 12px;
    background: #242444;
    border: 1px solid #3a3a5a;
    border-radius: 8px;
    color: #e0e0e0;
    font-size: 14px;
    font-family: 'Segoe UI', sans-serif;
    outline: none;
    transition: border-color 0.2s;
  }
  .field input:focus, .field select:focus { border-color: #6c63ff; }
  .field select {
    appearance: none;
    background-image: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' width='12' height='12' viewBox='0 0 12 12'%3E%3Cpath fill='%23888' d='M2 4l4 4 4-4'/%3E%3C/svg%3E");
    background-repeat: no-repeat;
    background-position: right 12px center;
    padding-right: 32px;
  }
  .field select option { background: #242444; color: #e0e0e0; }

  .btn-row { display: flex; gap: 10px; margin-top: 20px; justify-content: flex-end; }
  .btn {
    padding: 10px 28px;
    border: none;
    border-radius: 8px;
    font-size: 14px;
    font-weight: 600;
    cursor: pointer;
    transition: all 0.15s;
    font-family: 'Segoe UI', sans-serif;
  }
  .btn-primary { background: #6c63ff; color: #fff; }
  .btn-primary:hover { background: #5b52ee; }
  .btn-secondary { background: #2a2a4a; color: #ccc; }
  .btn-secondary:hover { background: #3a3a5a; }
  .btn-ghost { background: transparent; color: #888; border: 1px solid #3a3a5a; }
  .btn-ghost:hover { background: #2a2a4a; color: #ccc; }

  /* Actions */
  .action-card {
    background: #242444;
    border: 1px solid #3a3a5a;
    border-radius: 12px;
    padding: 20px;
    margin-bottom: 12px;
    display: flex;
    justify-content: space-between;
    align-items: center;
  }
  .action-card h3 { font-size: 15px; font-weight: 600; }
  .action-card p { font-size: 12px; color: #888; margin-top: 4px; }
  .btn-action {
    padding: 10px 24px;
    border: none;
    border-radius: 8px;
    font-size: 13px;
    font-weight: 600;
    cursor: pointer;
    transition: all 0.15s;
    min-width: 100px;
    font-family: 'Segoe UI', sans-serif;
  }
  .btn-start { background: #2d8a4e; color: #fff; }
  .btn-start:hover { background: #34a058; }
  .btn-stop { background: #c0392b; color: #fff; }
  .btn-stop:hover { background: #e74c3c; }
  .btn-toggle { background: #2a6ab5; color: #fff; }
  .btn-toggle:hover { background: #3480d0; }

  /* Message box */
  .message-box {
    background: #242444;
    border: 1px solid #3a3a5a;
    border-radius: 12px;
    padding: 12px 16px;
    max-height: 260px;
    overflow-y: auto;
    font-size: 13px;
    line-height: 1.5;
  }
  .message-box::-webkit-scrollbar { width: 6px; }
  .message-box::-webkit-scrollbar-track { background: transparent; }
  .message-box::-webkit-scrollbar-thumb { background: #3a3a5a; border-radius: 3px; }
  .message-line {
    padding: 4px 0;
    border-bottom: 1px solid #2a2a4a;
    color: #ccc;
  }
  .message-line:last-child { border-bottom: none; }
  .message-empty { color: #666; font-style: italic; }

  .toast {
    position: fixed;
    bottom: 20px;
    left: 50%;
    transform: translateX(-50%) translateY(60px);
    background: #2d8a4e;
    color: #fff;
    padding: 10px 24px;
    border-radius: 8px;
    font-size: 13px;
    font-weight: 600;
    opacity: 0;
    transition: all 0.3s;
    pointer-events: none;
    z-index: 100;
  }
  .toast.show { opacity: 1; transform: translateX(-50%) translateY(0); }
</style>
</head>
<body>
  <!-- Custom title bar -->
  <div class="titlebar">
    <span class="titlebar-title">LiveTranslator</span>
    <div class="titlebar-drag" onmousedown="dragWindow()"></div>
    <div class="titlebar-buttons">
      <button class="titlebar-btn" onclick="minimizeWindow()" title="Minimize">&#x2212;</button>
      <button class="titlebar-btn close" onclick="closeWindow()" title="Close">&#x2715;</button>
    </div>
  </div>

  <div class="tabs">
    <div class="tab active" onclick="switchTab('actions')">Actions</div>
    <div class="tab" onclick="switchTab('settings')">Settings</div>
  </div>

  <div id="actions" class="panel active">
    <div class="action-card">
      <div>
        <h3>Audio Capture</h3>
        <p>Start or stop capturing system audio for transcription</p>
      </div>
      <button id="captureBtn" class="btn-action btn-start" onclick="toggleCapture()">Start</button>
    </div>
    <div class="action-card">
      <div>
        <h3>Subtitle Overlay</h3>
        <p>Show or hide the subtitle overlay on screen</p>
      </div>
      <button class="btn-action btn-toggle" onclick="toggleOverlay()">Toggle</button>
    </div>

    <h2>RECENT TRANSCRIPTIONS</h2>
    <div id="messageBox" class="message-box">
      <div class="message-empty">No transcriptions yet</div>
    </div>
  </div>

  <div id="settings" class="panel">
    <h2>AUDIO</h2>
    <div class="field">
      <label>Audio Device</label>
      <select id="device"></select>
    </div>

    <h2>WHISPER API</h2>
    <div class="row">
      <div class="field">
        <label>API URL</label>
        <input id="url" type="text">
      </div>
    </div>
    <div class="row">
      <div class="field">
        <label>Language</label>
        <select id="language"></select>
      </div>
      <div class="field">
        <label>Mode</label>
        <select id="mode">
          <option value="transcribe">Transcribe</option>
          <option value="translate">Translate to English</option>
        </select>
      </div>
    </div>

    <h2>OVERLAY APPEARANCE</h2>
    <div class="row">
      <div class="field">
        <label>Font Size</label>
        <input id="fontSize" type="number" min="8" max="72">
      </div>
      <div class="field">
        <label>Overlay Opacity (0-255)</label>
        <input id="overlayOpacity" type="number" min="0" max="255">
      </div>
      <div class="field">
        <label>Hotkey</label>
        <input id="hotkey" type="text">
      </div>
    </div>
    <div class="row">
      <div class="field">
        <label>Background Color</label>
        <input id="bgColor" type="color">
      </div>
      <div class="field">
        <label>Background Opacity (0-255)</label>
        <input id="bgOpacity" type="number" min="0" max="255">
      </div>
    </div>
    <div class="row">
      <div class="field">
        <label>Text Outline</label>
        <select id="textOutline">
          <option value="true">On</option>
          <option value="false">Off</option>
        </select>
      </div>
      <div class="field">
        <label>Outline Color</label>
        <input id="outlineColor" type="color">
      </div>
      <div class="field">
        <label>Outline Thickness</label>
        <input id="outlineThickness" type="number" min="1" max="8">
      </div>
    </div>

    <h2>AUDIO PROCESSING</h2>
    <div class="field">
      <label>Voice Isolation / Noise Reduction: <span id="noiseLabel">50</span>%</label>
      <input id="noiseReduction" type="range" min="0" max="100" step="1" oninput="document.getElementById('noiseLabel').textContent=this.value" style="width:100%;accent-color:#6c63ff;">
    </div>

    <h2>STARTUP</h2>
    <div class="row">
      <div class="field">
        <label>Auto-start Capture</label>
        <select id="autoCapture">
          <option value="true">On</option>
          <option value="false">Off</option>
        </select>
      </div>
      <div class="field">
        <label>Start with Windows</label>
        <select id="startWithWindows">
          <option value="true">On</option>
          <option value="false">Off</option>
        </select>
      </div>
    </div>

    <div class="btn-row">
      <button class="btn btn-ghost" onclick="resetForm()">Reset</button>
      <button class="btn btn-secondary" onclick="saveSettings()">Save</button>
      <button class="btn btn-primary" onclick="saveAndClose()">Save &amp; Close</button>
    </div>
  </div>

  <div id="toast" class="toast"></div>

<script>
  const languages = [
    ["", "Auto-detect"], ["en", "English"], ["es", "Spanish"], ["fr", "French"],
    ["de", "German"], ["it", "Italian"], ["pt", "Portuguese"], ["nl", "Dutch"],
    ["pl", "Polish"], ["ru", "Russian"], ["zh", "Chinese"], ["ja", "Japanese"],
    ["ko", "Korean"], ["ar", "Arabic"], ["hi", "Hindi"], ["no", "Norwegian"],
    ["sv", "Swedish"], ["da", "Danish"], ["fi", "Finnish"], ["uk", "Ukrainian"], ["tr", "Turkish"]
  ];

  let capturing = false;

  function switchTab(name) {
    document.querySelectorAll('.tab').forEach((t, i) => {
      t.classList.toggle('active', (name === 'actions' && i === 0) || (name === 'settings' && i === 1));
    });
    document.querySelectorAll('.panel').forEach(p => p.classList.remove('active'));
    document.getElementById(name).classList.add('active');
  }

  function showToast(msg, color) {
    const t = document.getElementById('toast');
    t.textContent = msg;
    t.style.background = color || '#2d8a4e';
    t.classList.add('show');
    setTimeout(() => t.classList.remove('show'), 2000);
  }

  function getFormData() {
    return JSON.stringify({
      audio_device_id: document.getElementById('device').value,
      whisper_url: document.getElementById('url').value,
      language: document.getElementById('language').value,
      mode: document.getElementById('mode').value,
      font_size: parseInt(document.getElementById('fontSize').value) || 22,
      overlay_opacity: parseInt(document.getElementById('overlayOpacity').value) || 230,
      hotkey: document.getElementById('hotkey').value,
      bg_color: document.getElementById('bgColor').value,
      bg_opacity: parseInt(document.getElementById('bgOpacity').value) || 200,
      text_outline: document.getElementById('textOutline').value === 'true',
      outline_color: document.getElementById('outlineColor').value,
      outline_thickness: parseInt(document.getElementById('outlineThickness').value) || 1,
      noise_reduction: parseInt(document.getElementById('noiseReduction').value) || 50,
      auto_capture: document.getElementById('autoCapture').value === 'true',
      start_with_windows: document.getElementById('startWithWindows').value === 'true'
    });
  }

  async function saveSettings() {
    const result = JSON.parse(await saveConfig(getFormData()));
    if (result.ok) showToast('Settings saved');
    else showToast('Error: ' + result.error, '#c0392b');
  }

  async function saveAndClose() {
    const result = JSON.parse(await saveConfig(getFormData()));
    if (result.ok) closeWindow();
    else showToast('Error: ' + result.error, '#c0392b');
  }

  function resetForm() {
    loadConfig();
    showToast('Reset to saved values', '#2a6ab5');
  }

  function toggleCapture() {
    capturing = !capturing;
    const btn = document.getElementById('captureBtn');
    if (capturing) {
      startCapture();
      btn.textContent = 'Stop';
      btn.className = 'btn-action btn-stop';
      showToast('Capture started');
    } else {
      stopCapture();
      btn.textContent = 'Start';
      btn.className = 'btn-action btn-start';
      showToast('Capture stopped');
    }
  }

  async function loadConfig() {
    const cfg = JSON.parse(await getConfig());
    const devices = JSON.parse(await getDevices());

    const devSel = document.getElementById('device');
    devSel.innerHTML = '<option value="">Default</option>';
    devices.forEach(d => {
      const opt = document.createElement('option');
      opt.value = d.ID;
      opt.textContent = d.Name;
      if (d.ID === cfg.audio_device_id) opt.selected = true;
      devSel.appendChild(opt);
    });

    const langSel = document.getElementById('language');
    langSel.innerHTML = '';
    languages.forEach(([code, name]) => {
      const opt = document.createElement('option');
      opt.value = code;
      opt.textContent = name;
      if (code === cfg.language) opt.selected = true;
      langSel.appendChild(opt);
    });

    document.getElementById('url').value = cfg.whisper_url;
    document.getElementById('mode').value = cfg.mode;
    document.getElementById('fontSize').value = cfg.font_size;
    document.getElementById('overlayOpacity').value = cfg.overlay_opacity;
    document.getElementById('hotkey').value = cfg.hotkey;
    document.getElementById('bgColor').value = cfg.bg_color;
    document.getElementById('bgOpacity').value = cfg.bg_opacity;
    document.getElementById('textOutline').value = String(cfg.text_outline);
    document.getElementById('outlineColor').value = cfg.outline_color;
    document.getElementById('outlineThickness').value = cfg.outline_thickness || 1;
    document.getElementById('noiseReduction').value = cfg.noise_reduction ?? 50;
    document.getElementById('noiseLabel').textContent = cfg.noise_reduction ?? 50;
    document.getElementById('autoCapture').value = String(cfg.auto_capture);
    document.getElementById('startWithWindows').value = String(cfg.start_with_windows);
  }

  async function syncCaptureState() {
    capturing = await isCapturing();
    const btn = document.getElementById('captureBtn');
    if (capturing) {
      btn.textContent = 'Stop';
      btn.className = 'btn-action btn-stop';
    } else {
      btn.textContent = 'Start';
      btn.className = 'btn-action btn-start';
    }
  }

  let lastMsgCount = 0;
  async function pollMessages() {
    try {
      const msgs = JSON.parse(await getMessages());
      if (msgs && msgs.length !== lastMsgCount) {
        lastMsgCount = msgs.length;
        const box = document.getElementById('messageBox');
        if (msgs.length === 0) {
          box.innerHTML = '<div class="message-empty">No transcriptions yet</div>';
        } else {
          box.innerHTML = msgs.map(m => '<div class="message-line">' + escapeHtml(m) + '</div>').join('');
          box.scrollTop = box.scrollHeight;
        }
      }
    } catch(e) {}
  }
  function escapeHtml(s) {
    const d = document.createElement('div');
    d.textContent = s;
    return d.innerHTML;
  }
  setInterval(pollMessages, 1000);

  loadConfig();
  syncCaptureState();
</script>
</body>
</html>`
