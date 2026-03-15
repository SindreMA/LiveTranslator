package settings

import (
	"fmt"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/SindreMA/LiveTranslator/internal/audio"
	"github.com/SindreMA/LiveTranslator/internal/config"
)

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")
	gdi32    = windows.NewLazySystemDLL("gdi32.dll")
	dwm      = windows.NewLazySystemDLL("dwmapi.dll")
	uxtheme  = windows.NewLazySystemDLL("uxtheme.dll")

	procCreateWindowExW      = user32.NewProc("CreateWindowExW")
	procRegisterClassExW     = user32.NewProc("RegisterClassExW")
	procDefWindowProcW       = user32.NewProc("DefWindowProcW")
	procGetMessageW          = user32.NewProc("GetMessageW")
	procTranslateMessage     = user32.NewProc("TranslateMessage")
	procDispatchMessageW     = user32.NewProc("DispatchMessageW")
	procPostQuitMessage      = user32.NewProc("PostQuitMessage")
	procShowWindow           = user32.NewProc("ShowWindow")
	procUpdateWindow         = user32.NewProc("UpdateWindow")
	procDestroyWindow        = user32.NewProc("DestroyWindow")
	procSendMessageW         = user32.NewProc("SendMessageW")
	procGetWindowTextW       = user32.NewProc("GetWindowTextW")
	procGetWindowTextLengthW = user32.NewProc("GetWindowTextLengthW")
	procSetForegroundWindow  = user32.NewProc("SetForegroundWindow")
	procGetModuleHandleW     = kernel32.NewProc("GetModuleHandleW")
	procCreateFontW          = gdi32.NewProc("CreateFontW")
	procCreateSolidBrush     = gdi32.NewProc("CreateSolidBrush")
	procDeleteObject         = gdi32.NewProc("DeleteObject")
	procDwmSetWindowAttribute = dwm.NewProc("DwmSetWindowAttribute")
	procSetWindowTheme       = uxtheme.NewProc("SetWindowTheme")
)

const (
	WS_OVERLAPPEDWINDOW = 0x00CF0000
	WS_CAPTION          = 0x00C00000
	WS_SYSMENU          = 0x00080000
	WS_MINIMIZEBOX      = 0x00020000
	WS_VISIBLE          = 0x10000000
	WS_CHILD            = 0x40000000
	WS_TABSTOP          = 0x00010000
	WS_BORDER           = 0x00800000
	WS_VSCROLL          = 0x00200000
	WS_CLIPCHILDREN     = 0x02000000
	WS_EX_CLIENTEDGE    = 0x00000200

	WM_DESTROY   = 0x0002
	WM_CLOSE     = 0x0010
	WM_COMMAND   = 0x0111
	WM_SETFONT   = 0x0030
	WM_CTLCOLORSTATIC = 0x0138
	WM_CTLCOLOREDIT   = 0x0133
	WM_CTLCOLORBTN    = 0x0135

	BS_PUSHBUTTON    = 0x00000000
	BS_DEFPUSHBUTTON = 0x00000001
	ES_AUTOHSCROLL   = 0x0080

	CBS_DROPDOWNLIST = 0x0003
	CBS_HASSTRINGS   = 0x0200

	CB_ADDSTRING = 0x0143
	CB_SETCURSEL = 0x014E
	CB_GETCURSEL = 0x0147

	SS_LEFT = 0x0000

	SW_SHOW = 5

	BN_CLICKED = 0

	// Dark theme colors (BGR format for Win32).
	CLR_BG       = 0x00201a18 // dark bg
	CLR_SURFACE  = 0x00302820 // slightly lighter
	CLR_TEXT     = 0x00f0f0f0 // near-white
	CLR_ACCENT   = 0x00c89040 // teal/gold accent
	CLR_BTN_SAVE = 0x00a07830 // save button

	// DWM attributes for dark title bar.
	DWMWA_USE_IMMERSIVE_DARK_MODE = 20

	IDC_DEVICE_COMBO   = 1001
	IDC_URL_EDIT       = 1002
	IDC_LANG_COMBO     = 1003
	IDC_MODE_COMBO     = 1004
	IDC_FONTSIZE_EDIT  = 1005
	IDC_OPACITY_EDIT   = 1006
	IDC_HOTKEY_EDIT    = 1007
	IDC_BGCOLOR_EDIT   = 1008
	IDC_BGOPACITY_EDIT = 1009
	IDC_OUTLINE_COMBO    = 1012
	IDC_OUTLINECOLOR_EDIT = 1013
	IDC_SAVE_BTN       = 1010
	IDC_CANCEL_BTN     = 1011
)

type wndClassEx struct {
	CbSize        uint32
	Style         uint32
	LpfnWndProc   uintptr
	CbClsExtra    int32
	CbWndExtra    int32
	HInstance     uintptr
	HIcon         uintptr
	HCursor       uintptr
	HbrBackground uintptr
	LpszMenuName  *uint16
	LpszClassName *uint16
	HIconSm      uintptr
}

type msg struct {
	HWnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      struct{ X, Y int32 }
}

type OnSave func(cfg *config.Config)

var (
	settingsHwnd uintptr
	currentCfg   *config.Config
	devices      []audio.Device
	onSaveCb     OnSave
	controls     map[int]uintptr
	hBgBrush     uintptr
	hSurfBrush   uintptr
	hTitleFont   uintptr
	hLabelFont   uintptr
	hInputFont   uintptr
)

var languages = []struct {
	Code string
	Name string
}{
	{"", "Auto-detect"},
	{"en", "English"},
	{"es", "Spanish"},
	{"fr", "French"},
	{"de", "German"},
	{"it", "Italian"},
	{"pt", "Portuguese"},
	{"nl", "Dutch"},
	{"pl", "Polish"},
	{"ru", "Russian"},
	{"zh", "Chinese"},
	{"ja", "Japanese"},
	{"ko", "Korean"},
	{"ar", "Arabic"},
	{"hi", "Hindi"},
	{"no", "Norwegian"},
	{"sv", "Swedish"},
	{"da", "Danish"},
	{"fi", "Finnish"},
	{"uk", "Ukrainian"},
	{"tr", "Turkish"},
}

func Show(cfg *config.Config, saveCb OnSave) {
	currentCfg = cfg
	onSaveCb = saveCb
	controls = make(map[int]uintptr)

	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()

		devs, err := audio.ListRenderDevices()
		if err != nil {
			devs = nil
		}
		devices = devs

		createSettingsWindow()
	}()
}

func createSettingsWindow() {
	hInstance, _, _ := procGetModuleHandleW.Call(0)
	className, _ := syscall.UTF16PtrFromString("LTSettings2")
	wndProc := syscall.NewCallback(settingsWndProc)

	// Brushes for dark background.
	hBgBrush, _, _ = procCreateSolidBrush.Call(CLR_BG)
	hSurfBrush, _, _ = procCreateSolidBrush.Call(CLR_SURFACE)

	// Fonts.
	segoe, _ := syscall.UTF16PtrFromString("Segoe UI")
	hTitleFont, _, _ = procCreateFontW.Call(
		uintptr(uint32(0xFFFFFFE4)), 0, 0, 0, // -28
		700, 0, 0, 0, 0, 0, 0, 5, 0, uintptr(unsafe.Pointer(segoe)),
	)
	hLabelFont, _, _ = procCreateFontW.Call(
		uintptr(uint32(0xFFFFFFF2)), 0, 0, 0, // -14
		400, 0, 0, 0, 0, 0, 0, 5, 0, uintptr(unsafe.Pointer(segoe)),
	)
	hInputFont, _, _ = procCreateFontW.Call(
		uintptr(uint32(0xFFFFFFF1)), 0, 0, 0, // -15
		400, 0, 0, 0, 0, 0, 0, 5, 0, uintptr(unsafe.Pointer(segoe)),
	)

	wcx := wndClassEx{
		CbSize:        uint32(unsafe.Sizeof(wndClassEx{})),
		LpfnWndProc:   wndProc,
		HInstance:     hInstance,
		LpszClassName: className,
		HbrBackground: hBgBrush,
	}
	procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wcx)))

	windowName, _ := syscall.UTF16PtrFromString("LiveTranslator Settings")
	style := uintptr(WS_CAPTION | WS_SYSMENU | WS_MINIMIZEBOX | WS_CLIPCHILDREN)
	hwnd, _, _ := procCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(windowName)),
		style,
		150, 100, 520, 710,
		0, 0, hInstance, 0,
	)
	if hwnd == 0 {
		return
	}
	settingsHwnd = hwnd

	// Enable dark title bar on Windows 10/11.
	val := uint32(1)
	procDwmSetWindowAttribute.Call(hwnd, DWMWA_USE_IMMERSIVE_DARK_MODE, uintptr(unsafe.Pointer(&val)), 4)

	// Layout.
	const (
		marginX  = 28
		labelY   = 0
		inputY   = 20
		rowH     = 58
		inputW   = 440
		inputH   = 30
		sectionGap = 12
	)

	y := int32(20)

	// Title.
	createStatic(hwnd, hInstance, hTitleFont, "Settings", marginX, y, inputW, 36)
	y += 44

	// --- Audio section ---
	createStatic(hwnd, hInstance, hLabelFont, "AUDIO DEVICE", marginX, y, inputW, 18)
	y += 22
	combo := createCombo(hwnd, hInstance, hInputFont, marginX, y, inputW, 200, IDC_DEVICE_COMBO)
	setDarkTheme(combo)
	addComboString(combo, "Default")
	selectedDevice := 0
	for i, d := range devices {
		addComboString(combo, d.Name)
		if d.ID == currentCfg.AudioDeviceID {
			selectedDevice = i + 1
		}
	}
	procSendMessageW.Call(combo, CB_SETCURSEL, uintptr(selectedDevice), 0)
	y += rowH

	// API URL.
	createStatic(hwnd, hInstance, hLabelFont, "WHISPER API URL", marginX, y, inputW, 18)
	y += 22
	createEdit(hwnd, hInstance, hInputFont, currentCfg.WhisperURL, marginX, y, inputW, inputH, IDC_URL_EDIT)
	y += rowH

	// Language + Mode side by side.
	halfW := int32((inputW - 16) / 2)
	createStatic(hwnd, hInstance, hLabelFont, "LANGUAGE", marginX, y, halfW, 18)
	createStatic(hwnd, hInstance, hLabelFont, "MODE", marginX+halfW+16, y, halfW, 18)
	y += 22
	langCombo := createCombo(hwnd, hInstance, hInputFont, marginX, y, halfW, 300, IDC_LANG_COMBO)
	setDarkTheme(langCombo)
	selectedLang := 0
	for i, l := range languages {
		addComboString(langCombo, l.Name)
		if l.Code == currentCfg.Language {
			selectedLang = i
		}
	}
	procSendMessageW.Call(langCombo, CB_SETCURSEL, uintptr(selectedLang), 0)

	modeCombo := createCombo(hwnd, hInstance, hInputFont, marginX+halfW+16, y, halfW, 100, IDC_MODE_COMBO)
	setDarkTheme(modeCombo)
	addComboString(modeCombo, "Transcribe")
	addComboString(modeCombo, "Translate to English")
	modeIdx := uintptr(0)
	if currentCfg.Mode == "translate" {
		modeIdx = 1
	}
	procSendMessageW.Call(modeCombo, CB_SETCURSEL, modeIdx, 0)
	y += rowH

	// Font Size + Overlay Opacity side by side.
	thirdW := int32((inputW - 32) / 3)
	createStatic(hwnd, hInstance, hLabelFont, "FONT SIZE", marginX, y, thirdW, 18)
	createStatic(hwnd, hInstance, hLabelFont, "OVERLAY OPACITY", marginX+thirdW+16, y, thirdW, 18)
	createStatic(hwnd, hInstance, hLabelFont, "HOTKEY", marginX+thirdW*2+32, y, thirdW, 18)
	y += 22
	createEdit(hwnd, hInstance, hInputFont, fmt.Sprintf("%d", currentCfg.FontSize), marginX, y, thirdW, inputH, IDC_FONTSIZE_EDIT)
	createEdit(hwnd, hInstance, hInputFont, fmt.Sprintf("%d", currentCfg.OverlayOpacity), marginX+thirdW+16, y, thirdW, inputH, IDC_OPACITY_EDIT)
	createEdit(hwnd, hInstance, hInputFont, currentCfg.Hotkey, marginX+thirdW*2+32, y, thirdW, inputH, IDC_HOTKEY_EDIT)
	y += rowH

	// Background color + Background opacity side by side.
	createStatic(hwnd, hInstance, hLabelFont, "BG COLOR (hex)", marginX, y, halfW, 18)
	createStatic(hwnd, hInstance, hLabelFont, "BG OPACITY (0-255)", marginX+halfW+16, y, halfW, 18)
	y += 22
	createEdit(hwnd, hInstance, hInputFont, currentCfg.BgColor, marginX, y, halfW, inputH, IDC_BGCOLOR_EDIT)
	createEdit(hwnd, hInstance, hInputFont, fmt.Sprintf("%d", currentCfg.BgOpacity), marginX+halfW+16, y, halfW, inputH, IDC_BGOPACITY_EDIT)
	y += rowH

	// Text outline + Outline color.
	createStatic(hwnd, hInstance, hLabelFont, "TEXT OUTLINE", marginX, y, halfW, 18)
	createStatic(hwnd, hInstance, hLabelFont, "OUTLINE COLOR (hex)", marginX+halfW+16, y, halfW, 18)
	y += 22
	outlineCombo := createCombo(hwnd, hInstance, hInputFont, marginX, y, halfW, 80, IDC_OUTLINE_COMBO)
	setDarkTheme(outlineCombo)
	addComboString(outlineCombo, "Off")
	addComboString(outlineCombo, "On")
	outlineIdx := uintptr(0)
	if currentCfg.TextOutline {
		outlineIdx = 1
	}
	procSendMessageW.Call(outlineCombo, CB_SETCURSEL, outlineIdx, 0)
	createEdit(hwnd, hInstance, hInputFont, currentCfg.OutlineColor, marginX+halfW+16, y, halfW, inputH, IDC_OUTLINECOLOR_EDIT)
	y += rowH + sectionGap

	// Buttons.
	btnW := int32(130)
	btnH := int32(38)
	btnGap := int32(12)
	totalBtnW := btnW*2 + btnGap
	btnX := marginX + (inputW-totalBtnW)/2
	createButton(hwnd, hInstance, hInputFont, "Save", btnX, y, btnW, btnH, IDC_SAVE_BTN, true)
	createButton(hwnd, hInstance, hInputFont, "Cancel", btnX+btnW+btnGap, y, btnW, btnH, IDC_CANCEL_BTN, false)

	procShowWindow.Call(hwnd, SW_SHOW)
	procUpdateWindow.Call(hwnd)
	procSetForegroundWindow.Call(hwnd)

	var m msg
	for {
		ret, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if ret == 0 || int32(ret) == -1 {
			break
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}

	// Cleanup.
	procDeleteObject.Call(hBgBrush)
	procDeleteObject.Call(hSurfBrush)
	procDeleteObject.Call(hTitleFont)
	procDeleteObject.Call(hLabelFont)
	procDeleteObject.Call(hInputFont)
	settingsHwnd = 0
}

func settingsWndProc(hwnd uintptr, umsg uint32, wParam, lParam uintptr) uintptr {
	switch umsg {
	case WM_COMMAND:
		id := int(wParam & 0xFFFF)
		notification := int((wParam >> 16) & 0xFFFF)
		if notification == BN_CLICKED {
			switch id {
			case IDC_SAVE_BTN:
				saveSettings()
				procDestroyWindow.Call(hwnd)
				return 0
			case IDC_CANCEL_BTN:
				procDestroyWindow.Call(hwnd)
				return 0
			}
		}

	case WM_CTLCOLORSTATIC:
		// Dark background + light text for labels.
		hdc := wParam
		setTextColorProc.Call(hdc, CLR_TEXT)
		setBkModeProc.Call(hdc, 1) // TRANSPARENT
		return hBgBrush

	case WM_CTLCOLOREDIT:
		hdc := wParam
		setTextColorProc.Call(hdc, CLR_TEXT)
		setBkColorProc.Call(hdc, CLR_SURFACE)
		return hSurfBrush

	case WM_CLOSE:
		procDestroyWindow.Call(hwnd)
		return 0

	case WM_DESTROY:
		procPostQuitMessage.Call(0)
		return 0
	}

	ret, _, _ := procDefWindowProcW.Call(hwnd, uintptr(umsg), wParam, lParam)
	return ret
}

var (
	setTextColorProc = gdi32.NewProc("SetTextColor")
	setBkModeProc    = gdi32.NewProc("SetBkMode")
	setBkColorProc   = gdi32.NewProc("SetBkColor")
)

func saveSettings() {
	cfg := *currentCfg

	idx, _, _ := procSendMessageW.Call(controls[IDC_DEVICE_COMBO], CB_GETCURSEL, 0, 0)
	if int(idx) <= 0 {
		cfg.AudioDeviceID = ""
	} else if int(idx)-1 < len(devices) {
		cfg.AudioDeviceID = devices[int(idx)-1].ID
	}

	cfg.WhisperURL = getEditText(controls[IDC_URL_EDIT])

	langIdx, _, _ := procSendMessageW.Call(controls[IDC_LANG_COMBO], CB_GETCURSEL, 0, 0)
	if int(langIdx) >= 0 && int(langIdx) < len(languages) {
		cfg.Language = languages[int(langIdx)].Code
	}

	modeIdx, _, _ := procSendMessageW.Call(controls[IDC_MODE_COMBO], CB_GETCURSEL, 0, 0)
	if int(modeIdx) == 1 {
		cfg.Mode = "translate"
	} else {
		cfg.Mode = "transcribe"
	}

	if fs := getEditText(controls[IDC_FONTSIZE_EDIT]); fs != "" {
		var n int
		if _, err := fmt.Sscanf(fs, "%d", &n); err == nil && n >= 8 && n <= 72 {
			cfg.FontSize = n
		}
	}

	if op := getEditText(controls[IDC_OPACITY_EDIT]); op != "" {
		var n int
		if _, err := fmt.Sscanf(op, "%d", &n); err == nil && n >= 0 && n <= 255 {
			cfg.OverlayOpacity = n
		}
	}

	if hk := getEditText(controls[IDC_HOTKEY_EDIT]); hk != "" {
		cfg.Hotkey = hk
	}

	if bg := getEditText(controls[IDC_BGCOLOR_EDIT]); bg != "" {
		cfg.BgColor = bg
	}

	if bo := getEditText(controls[IDC_BGOPACITY_EDIT]); bo != "" {
		var n int
		if _, err := fmt.Sscanf(bo, "%d", &n); err == nil && n >= 0 && n <= 255 {
			cfg.BgOpacity = n
		}
	}

	outlineIdx, _, _ := procSendMessageW.Call(controls[IDC_OUTLINE_COMBO], CB_GETCURSEL, 0, 0)
	cfg.TextOutline = int(outlineIdx) == 1

	if oc := getEditText(controls[IDC_OUTLINECOLOR_EDIT]); oc != "" {
		cfg.OutlineColor = oc
	}

	if err := cfg.Save(); err == nil && onSaveCb != nil {
		onSaveCb(&cfg)
	}
}

func getEditText(hwnd uintptr) string {
	length, _, _ := procGetWindowTextLengthW.Call(hwnd)
	if length == 0 {
		return ""
	}
	buf := make([]uint16, length+1)
	procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(length+1))
	return syscall.UTF16ToString(buf)
}

func setDarkTheme(hwnd uintptr) {
	dark, _ := syscall.UTF16PtrFromString("DarkMode_Explorer")
	procSetWindowTheme.Call(hwnd, uintptr(unsafe.Pointer(dark)), 0)
}

func createStatic(parent, hInstance, hFont uintptr, text string, x, y, w, h int32) uintptr {
	cls, _ := syscall.UTF16PtrFromString("STATIC")
	txt, _ := syscall.UTF16PtrFromString(text)
	hwnd, _, _ := procCreateWindowExW.Call(0,
		uintptr(unsafe.Pointer(cls)),
		uintptr(unsafe.Pointer(txt)),
		WS_CHILD|WS_VISIBLE|SS_LEFT,
		uintptr(x), uintptr(y), uintptr(w), uintptr(h),
		parent, 0, hInstance, 0,
	)
	procSendMessageW.Call(hwnd, WM_SETFONT, hFont, 1)
	return hwnd
}

func createEdit(parent, hInstance, hFont uintptr, text string, x, y, w, h int32, id int) uintptr {
	cls, _ := syscall.UTF16PtrFromString("EDIT")
	txt, _ := syscall.UTF16PtrFromString(text)
	hwnd, _, _ := procCreateWindowExW.Call(
		WS_EX_CLIENTEDGE,
		uintptr(unsafe.Pointer(cls)),
		uintptr(unsafe.Pointer(txt)),
		WS_CHILD|WS_VISIBLE|WS_TABSTOP|ES_AUTOHSCROLL,
		uintptr(x), uintptr(y), uintptr(w), uintptr(h),
		parent, uintptr(id), hInstance, 0,
	)
	procSendMessageW.Call(hwnd, WM_SETFONT, hFont, 1)
	setDarkTheme(hwnd)
	controls[id] = hwnd
	return hwnd
}

func createCombo(parent, hInstance, hFont uintptr, x, y, w, h int32, id int) uintptr {
	cls, _ := syscall.UTF16PtrFromString("COMBOBOX")
	hwnd, _, _ := procCreateWindowExW.Call(0,
		uintptr(unsafe.Pointer(cls)),
		0,
		WS_CHILD|WS_VISIBLE|WS_TABSTOP|CBS_DROPDOWNLIST|CBS_HASSTRINGS|WS_VSCROLL,
		uintptr(x), uintptr(y), uintptr(w), uintptr(h),
		parent, uintptr(id), hInstance, 0,
	)
	procSendMessageW.Call(hwnd, WM_SETFONT, hFont, 1)
	controls[id] = hwnd
	return hwnd
}

func addComboString(combo uintptr, text string) {
	t, _ := syscall.UTF16PtrFromString(text)
	procSendMessageW.Call(combo, CB_ADDSTRING, 0, uintptr(unsafe.Pointer(t)))
}

func createButton(parent, hInstance, hFont uintptr, text string, x, y, w, h int32, id int, defBtn bool) uintptr {
	cls, _ := syscall.UTF16PtrFromString("BUTTON")
	txt, _ := syscall.UTF16PtrFromString(text)
	style := uintptr(WS_CHILD | WS_VISIBLE | WS_TABSTOP | BS_PUSHBUTTON)
	if defBtn {
		style = uintptr(WS_CHILD | WS_VISIBLE | WS_TABSTOP | BS_DEFPUSHBUTTON)
	}
	hwnd, _, _ := procCreateWindowExW.Call(0,
		uintptr(unsafe.Pointer(cls)),
		uintptr(unsafe.Pointer(txt)),
		style,
		uintptr(x), uintptr(y), uintptr(w), uintptr(h),
		parent, uintptr(id), hInstance, 0,
	)
	procSendMessageW.Call(hwnd, WM_SETFONT, hFont, 1)
	setDarkTheme(hwnd)
	controls[id] = hwnd
	return hwnd
}
