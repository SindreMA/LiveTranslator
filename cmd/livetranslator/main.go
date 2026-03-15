package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/SindreMA/LiveTranslator/internal/audio"
	"github.com/SindreMA/LiveTranslator/internal/config"
	"github.com/SindreMA/LiveTranslator/internal/icon"
	"github.com/SindreMA/LiveTranslator/internal/overlay"
	ltray "github.com/SindreMA/LiveTranslator/internal/tray"
	"github.com/SindreMA/LiveTranslator/internal/ui"
	"github.com/SindreMA/LiveTranslator/internal/whisper"

	"golang.design/x/hotkey"
)

type errorNotifier struct {
	mu       sync.Mutex
	lastTime time.Time
	cooldown time.Duration
	tray     *ltray.Tray
}

func newErrorNotifier(t *ltray.Tray, cooldown time.Duration) *errorNotifier {
	return &errorNotifier{tray: t, cooldown: cooldown}
}

func (e *errorNotifier) notify(title, message string) {
	log.Printf("ERROR: %s: %s", title, message)
	e.mu.Lock()
	defer e.mu.Unlock()
	if time.Since(e.lastTime) < e.cooldown {
		return
	}
	e.lastTime = time.Now()
	if e.tray != nil {
		e.tray.Notify(title, message)
	}
}

func main() {
	log.SetFlags(log.Ltime | log.Lmicroseconds)
	log.Println("LiveTranslator starting...")

	cfg, err := config.Load()
	if err != nil {
		log.Printf("Warning: failed to load config: %v (using defaults)", err)
	}
	log.Printf("Config loaded: API=%s, Mode=%s, Device=%q, Hotkey=%s",
		cfg.WhisperURL, cfg.Mode, cfg.AudioDeviceID, cfg.Hotkey)

	iconData := icon.GenerateICO()

	// Pipeline state.
	var (
		mu             sync.Mutex
		pipelineActive bool
		captureStop    = make(chan struct{})
		chunkerStop    = make(chan struct{})
		overlayStop    = make(chan struct{})
		pcmChan        = make(chan audio.CaptureResult, 64)
		wavChan        = make(chan []byte, 16)
		whisperClient  = whisper.NewClient(cfg.WhisperURL, cfg.Language, cfg.Mode)
		cancelPipeline context.CancelFunc
		appUI          *ui.Window
	)

	// Create overlay.
	log.Println("Creating overlay...")
	audio.SetNoiseReduction(cfg.NoiseReduction)
	ov := overlay.New(cfg.FontSize, cfg.OverlayOpacity, cfg.ParseBgColorBGR(), cfg.BgOpacity, cfg.TextOutline, cfg.ParseOutlineColorBGR(), cfg.OutlineThickness)
	overlayReady := make(chan struct{})
	go func() {
		if err := ov.Run(overlayReady, overlayStop); err != nil {
			log.Printf("Overlay error: %v", err)
		}
	}()
	<-overlayReady
	log.Println("Overlay ready")

	// Resolve device name.
	deviceName := resolveDeviceName(cfg.AudioDeviceID)

	var errNotify *errorNotifier

	// Pipeline control.
	startPipeline := func() {
		mu.Lock()
		defer mu.Unlock()
		if pipelineActive {
			return
		}
		log.Println("Starting capture pipeline...")
		captureStop = make(chan struct{})
		chunkerStop = make(chan struct{})
		pcmChan = make(chan audio.CaptureResult, 64)
		wavChan = make(chan []byte, 16)

		if err := audio.StartCapture(cfg.AudioDeviceID, pcmChan, captureStop); err != nil {
			log.Printf("ERROR: Failed to start capture: %v", err)
			if errNotify != nil {
				errNotify.notify("Error", "Capture failed: "+err.Error())
			}
			return
		}
		audio.StartChunker(pcmChan, wavChan, chunkerStop)
		var ctx context.Context
		ctx, cancelPipeline = context.WithCancel(context.Background())

		go func() {
			var lastText string
			var repeatCount int
			for {
				select {
				case <-ctx.Done():
					return
				case wav, ok := <-wavChan:
					if !ok {
						return
					}
					log.Printf("Sending %d bytes to whisper API...", len(wav))
					text, err := whisperClient.Transcribe(ctx, wav)
					if err != nil {
						if ctx.Err() == nil && errNotify != nil {
							errNotify.notify("Error", "Transcription failed: "+err.Error())
						}
						continue
					}
					text = strings.TrimSpace(text)
					if text == "" {
						continue
					}

					// Skip repeated text (whisper hallucination on silence).
					if text == lastText {
						repeatCount++
						if repeatCount >= 2 {
							log.Printf("Skipping repeated: %q (x%d)", text, repeatCount)
							continue
						}
					} else {
						repeatCount = 0
					}
					lastText = text

					log.Printf("Transcribed: %q", text)
					ov.SetText(text)
					appUI.AddMessage(text)
				}
			}
		}()
		pipelineActive = true
		log.Println("Pipeline started")
	}

	stopPipeline := func() {
		mu.Lock()
		defer mu.Unlock()
		if !pipelineActive {
			return
		}
		close(captureStop)
		close(chunkerStop)
		if cancelPipeline != nil {
			cancelPipeline()
		}
		pipelineActive = false
		log.Println("Pipeline stopped")
	}

	// Apply config changes from UI.
	applyConfig := func(newCfg *config.Config) {
		mu.Lock()
		oldDeviceID := cfg.AudioDeviceID
		*cfg = *newCfg
		whisperClient.BaseURL = cfg.WhisperURL
		whisperClient.Language = cfg.Language
		whisperClient.Task = cfg.Mode
		deviceChanged := oldDeviceID != cfg.AudioDeviceID
		needRestart := deviceChanged && pipelineActive
		mu.Unlock()

		ov.SetFontSize(cfg.FontSize)
		ov.SetOpacity(cfg.OverlayOpacity)
		ov.SetBgColor(cfg.ParseBgColorBGR())
		ov.SetBgOpacity(cfg.BgOpacity)
		ov.SetTextOutline(cfg.TextOutline, cfg.ParseOutlineColorBGR(), cfg.OutlineThickness)
		audio.SetNoiseReduction(cfg.NoiseReduction)

		// Restart capture pipeline if device changed while running.
		if needRestart {
			log.Printf("Device changed, restarting capture pipeline...")
			stopPipeline()
			startPipeline()
		}

		log.Printf("Config applied: API=%s, Mode=%s, FontSize=%d, Device=%q", cfg.WhisperURL, cfg.Mode, cfg.FontSize, cfg.AudioDeviceID)
	}

	// Tray reference (set after tray creation, used by UI + hotkey callbacks).
	var trayRef *ltray.Tray

	// UI window (webview-based).
	appUI = ui.New(ui.Callbacks{
		OnSave: func(newCfg *config.Config) {
			applyConfig(newCfg)
			// Update tray status.
			dn := resolveDeviceName(cfg.AudioDeviceID)
			if trayRef != nil {
				trayRef.UpdateState(ltray.State{
					DeviceName: dn,
					WhisperURL: cfg.WhisperURL,
					Language:   cfg.Language,
					Hotkey:     cfg.Hotkey,
				})
			}
		},
		OnStartCapture: func() {
			startPipeline()
			if trayRef != nil {
				trayRef.Notify("LiveTranslator", "Capture started")
			}
		},
		OnStopCapture: func() {
			stopPipeline()
			if trayRef != nil {
				trayRef.Notify("LiveTranslator", "Capture stopped")
			}
		},
		OnToggleOverlay: func() {
			ov.Toggle()
			if trayRef != nil {
				trayRef.SetOverlayVisible(ov.IsVisible())
			}
		},
		IsCapturing: func() bool {
			mu.Lock()
			defer mu.Unlock()
			return pipelineActive
		},
	})
	appUI.SetIconData(iconData)

	// Hotkey.
	go func() {
		hk := parseHotkey(cfg.Hotkey)
		if hk == nil {
			log.Printf("WARNING: Invalid hotkey config: %q", cfg.Hotkey)
			return
		}
		log.Printf("Registering hotkey: %s", cfg.Hotkey)
		if err := hk.Register(); err != nil {
			log.Printf("ERROR: Failed to register hotkey %s: %v", cfg.Hotkey, err)
			return
		}
		defer hk.Unregister()
		log.Printf("Hotkey %s registered", cfg.Hotkey)

		for range hk.Keydown() {
			ov.Toggle()
			if trayRef != nil {
				trayRef.SetOverlayVisible(ov.IsVisible())
			}
		}
	}()

	// Tray.
	var t *ltray.Tray
	t = ltray.New(ltray.Callbacks{
		OnStartStop: func(running bool) {
			if running {
				startPipeline()
				t.Notify("LiveTranslator", "Capture started")
			} else {
				stopPipeline()
				t.Notify("LiveTranslator", "Capture stopped")
			}
		},
		OnModeChange: func(mode string) {
			mu.Lock()
			whisperClient.Task = mode
			cfg.Mode = mode
			_ = cfg.Save()
			mu.Unlock()
			log.Printf("Mode changed to: %s", mode)
			t.Notify("LiveTranslator", "Mode: "+mode)
		},
		OnToggleOverlay: func() {
			ov.Toggle()
			t.SetOverlayVisible(ov.IsVisible())
		},
		OnOpen: func() {
			log.Println("Opening UI window...")
			appUI.Show(cfg)
		},
		OnQuit: func() {
			log.Println("Quitting...")
			stopPipeline()
			close(overlayStop)
		},
	}, cfg.Mode, ltray.State{
		DeviceName: deviceName,
		WhisperURL: cfg.WhisperURL,
		Language:   cfg.Language,
		Hotkey:     cfg.Hotkey,
	}, iconData)
	trayRef = t

	errNotify = newErrorNotifier(t, 30*time.Second)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Println("Signal received, shutting down...")
		stopPipeline()
		close(overlayStop)
		t.Quit()
	}()

	go func() {
		<-t.Ready()
		log.Println("System tray ready")
		t.Notify("LiveTranslator", "Running in system tray")

		if cfg.AutoCapture {
			log.Println("Auto-starting capture pipeline...")
			startPipeline()
			t.SetRunning(true)
			t.Notify("LiveTranslator", "Capture auto-started")
		}
	}()

	log.Println("Starting system tray...")
	t.Run()
	log.Println("LiveTranslator exited")
}

func resolveDeviceName(deviceID string) string {
	if deviceID == "" {
		return ""
	}
	if devs, err := audio.ListRenderDevices(); err == nil {
		for _, d := range devs {
			if d.ID == deviceID {
				return d.Name
			}
		}
	}
	return ""
}

func parseHotkey(spec string) *hotkey.Hotkey {
	parts := strings.Split(strings.ToLower(spec), "+")
	if len(parts) == 0 {
		return nil
	}

	var mods []hotkey.Modifier
	var key hotkey.Key

	for _, p := range parts {
		p = strings.TrimSpace(p)
		switch p {
		case "ctrl":
			mods = append(mods, hotkey.ModCtrl)
		case "shift":
			mods = append(mods, hotkey.ModShift)
		case "alt":
			mods = append(mods, hotkey.ModAlt)
		default:
			if len(p) == 1 && p[0] >= 'a' && p[0] <= 'z' {
				key = hotkey.Key(p[0] - 'a' + 0x41)
			}
		}
	}

	if key == 0 {
		return nil
	}

	return hotkey.New(mods, key)
}
