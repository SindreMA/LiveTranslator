package config

import (
	"encoding/json"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows/registry"
)

type Config struct {
	AudioDeviceID  string `json:"audio_device_id"`
	WhisperURL     string `json:"whisper_url"`
	Language       string `json:"language"`
	Mode           string `json:"mode"`
	FontSize       int    `json:"font_size"`
	OverlayOpacity int    `json:"overlay_opacity"`
	BgColor        string `json:"bg_color"`
	BgOpacity      int    `json:"bg_opacity"`
	TextOutline      bool   `json:"text_outline"`
	OutlineColor     string `json:"outline_color"`
	OutlineThickness int    `json:"outline_thickness"`
	NoiseReduction   int    `json:"noise_reduction"`
	Hotkey           string `json:"hotkey"`
	AutoCapture      bool   `json:"auto_capture"`
	StartWithWindows bool   `json:"start_with_windows"`
}

func DefaultConfig() *Config {
	return &Config{
		AudioDeviceID:  "",
		WhisperURL:     "http://localhost:8000",
		Language:       "",
		Mode:           "transcribe",
		FontSize:       22,
		OverlayOpacity: 230,
		BgColor:        "#1a1a2e",
		BgOpacity:      200,
		TextOutline:      true,
		OutlineColor:    "#000000",
		OutlineThickness: 1,
		NoiseReduction:  50,
		Hotkey:          "Ctrl+Shift+T",
	}
}

// ParseBgColorBGR converts BgColor hex "#RRGGBB" to Win32 BGR uint32.
func (c *Config) ParseBgColorBGR() uint32 {
	return parseHexBGR(c.BgColor, 0x002e1a1a)
}

// ParseOutlineColorBGR converts OutlineColor hex "#RRGGBB" to Win32 BGR uint32.
func (c *Config) ParseOutlineColorBGR() uint32 {
	return parseHexBGR(c.OutlineColor, 0x00000000)
}

func parseHexBGR(s string, fallback uint32) uint32 {
	if len(s) == 7 && s[0] == '#' {
		var r, g, b uint8
		for i, ch := range s[1:] {
			var v uint8
			switch {
			case ch >= '0' && ch <= '9':
				v = uint8(ch - '0')
			case ch >= 'a' && ch <= 'f':
				v = uint8(ch-'a') + 10
			case ch >= 'A' && ch <= 'F':
				v = uint8(ch-'A') + 10
			}
			switch i {
			case 0:
				r = v << 4
			case 1:
				r |= v
			case 2:
				g = v << 4
			case 3:
				g |= v
			case 4:
				b = v << 4
			case 5:
				b |= v
			}
		}
		return uint32(b)<<16 | uint32(g)<<8 | uint32(r)
	}
	return fallback
}

func configDir() (string, error) {
	appData := os.Getenv("APPDATA")
	if appData == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		appData = filepath.Join(home, "AppData", "Roaming")
	}
	dir := filepath.Join(appData, "LiveTranslator")
	return dir, os.MkdirAll(dir, 0755)
}

func configPath() (string, error) {
	dir, err := configDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

func Load() (*Config, error) {
	path, err := configPath()
	if err != nil {
		return DefaultConfig(), err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			cfg := DefaultConfig()
			_ = cfg.Save()
			return cfg, nil
		}
		return DefaultConfig(), err
	}

	cfg := DefaultConfig()
	if err := json.Unmarshal(data, cfg); err != nil {
		return DefaultConfig(), err
	}
	return cfg, nil
}

func (c *Config) Save() error {
	path, err := configPath()
	if err != nil {
		return err
	}

	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

const registryKey = `Software\Microsoft\Windows\CurrentVersion\Run`
const registryValueName = "LiveTranslator"

// ApplyStartWithWindows sets or removes the Windows startup registry entry
// based on the current config value.
func (c *Config) ApplyStartWithWindows() error {
	k, err := registry.OpenKey(registry.CURRENT_USER, registryKey, registry.SET_VALUE|registry.QUERY_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()

	if c.StartWithWindows {
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		return k.SetStringValue(registryValueName, exe)
	}

	// Remove the value; ignore error if it doesn't exist.
	err = k.DeleteValue(registryValueName)
	if err == registry.ErrNotExist {
		return nil
	}
	return err
}
