package tray

import (
	"fmt"
	"log"
	"runtime"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	shell32  = windows.NewLazySystemDLL("shell32.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")
	gdi32    = windows.NewLazySystemDLL("gdi32.dll")

	procRegisterClassExW         = user32.NewProc("RegisterClassExW")
	procCreateWindowExW          = user32.NewProc("CreateWindowExW")
	procDefWindowProcW           = user32.NewProc("DefWindowProcW")
	procGetMessageW              = user32.NewProc("GetMessageW")
	procTranslateMessage         = user32.NewProc("TranslateMessage")
	procDispatchMessageW         = user32.NewProc("DispatchMessageW")
	procPostMessageW             = user32.NewProc("PostMessageW")
	procPostQuitMessage          = user32.NewProc("PostQuitMessage")
	procDestroyWindow            = user32.NewProc("DestroyWindow")
	procCreatePopupMenu          = user32.NewProc("CreatePopupMenu")
	procInsertMenuItemW          = user32.NewProc("InsertMenuItemW")
	procTrackPopupMenuEx         = user32.NewProc("TrackPopupMenuEx")
	procDestroyMenu              = user32.NewProc("DestroyMenu")
	procSetForegroundWindow      = user32.NewProc("SetForegroundWindow")
	procGetCursorPos             = user32.NewProc("GetCursorPos")
	procCreateIconFromResourceEx = user32.NewProc("CreateIconFromResourceEx")
	procDestroyIcon              = user32.NewProc("DestroyIcon")
	procGetModuleHandleW         = kernel32.NewProc("GetModuleHandleW")
	procShellNotifyIconW         = shell32.NewProc("Shell_NotifyIconW")
	procGetStockObject           = gdi32.NewProc("GetStockObject")
)

const (
	NIM_ADD    = 0x00000000
	NIM_MODIFY = 0x00000001
	NIM_DELETE = 0x00000002

	NIF_MESSAGE = 0x00000001
	NIF_ICON    = 0x00000002
	NIF_TIP     = 0x00000004
	NIF_INFO    = 0x00000010

	NIIF_INFO     = 0x00000001
	NIIF_NOSOUND  = 0x00000010
	NIIF_RESPECT_QUIET_TIME = 0x00000080

	WM_USER         = 0x0400
	WM_APP          = 0x8000
	WM_TRAYICON     = WM_APP + 1
	WM_SHOW_BALLOON = WM_APP + 2
	WM_QUIT_TRAY    = WM_APP + 3
	WM_COMMAND      = 0x0111

	WM_LBUTTONUP     = 0x0202
	WM_LBUTTONDBLCLK = 0x0203
	WM_RBUTTONUP     = 0x0205
	WM_CONTEXTMENU   = 0x007B

	TPM_RIGHTBUTTON = 0x0002
	TPM_BOTTOMALIGN = 0x0020

	MFT_STRING    = 0x00000000
	MFT_SEPARATOR = 0x00000800
	MIIM_FTYPE    = 0x00000100
	MIIM_STRING   = 0x00000040
	MIIM_ID       = 0x00000002
	MIIM_STATE    = 0x00000001
	MFS_ENABLED   = 0x00000000
	MFS_DISABLED  = 0x00000003
	MFS_CHECKED   = 0x00000008
	MFS_GRAYED    = 0x00000001

	LR_DEFAULTCOLOR = 0x0000

	IDM_HEADER       = 100
	IDM_STATUS_CAP   = 101
	IDM_STATUS_OVL   = 102
	IDM_STATUS_MODE  = 103
	IDM_STATUS_DEV   = 104
	IDM_STATUS_API   = 105
	IDM_STATUS_LANG  = 106
	IDM_STATUS_HK    = 107
	IDM_SEP1         = 150
	IDM_START_STOP   = 200
	IDM_TOGGLE_OVL   = 201
	IDM_SEP2         = 250
	IDM_TRANSCRIBE   = 300
	IDM_TRANSLATE    = 301
	IDM_SEP3         = 350
	IDM_SETTINGS     = 400
	IDM_QUIT         = 500
)

// Win32 structs.

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

type notifyIconData struct {
	CbSize           uint32
	HWnd             uintptr
	UID              uint32
	UFlags           uint32
	UCallbackMessage uint32
	HIcon            uintptr
	SzTip            [128]uint16
	DwState          uint32
	DwStateMask      uint32
	SzInfo           [256]uint16
	UVersion         uint32
	SzInfoTitle      [64]uint16
	DwInfoFlags      uint32
	GuidItem         [16]byte
	HBalloonIcon     uintptr
}

type menuItemInfo struct {
	CbSize        uint32
	FMask         uint32
	FType         uint32
	FState        uint32
	WID           uint32
	HSubMenu      uintptr
	HbmpChecked   uintptr
	HbmpUnchecked uintptr
	DwItemData    uintptr
	DwTypeData    *uint16
	Cch           uint32
	HbmpItem      uintptr
}

type point struct{ X, Y int32 }

type msgT struct {
	HWnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      point
}

// Callbacks defines the actions the tray menu can trigger.
type Callbacks struct {
	OnStartStop     func(running bool)
	OnModeChange    func(mode string)
	OnToggleOverlay func()
	OnOpen          func() // double-click / "Open" menu item
	OnQuit          func()
}

// State holds the current application state for display.
type State struct {
	DeviceName string
	WhisperURL string
	Language   string
	Hotkey     string
}

type balloonMsg struct {
	title, message string
}

type Tray struct {
	cb             Callbacks
	running        bool
	overlayVisible bool
	mode           string
	state          State
	iconData       []byte

	hwnd      uintptr
	hIcon     uintptr
	iconAdded bool
	readyCh   chan struct{}
	mu        sync.Mutex
	balloons  []balloonMsg
}

func New(cb Callbacks, initialMode string, initialState State, iconData []byte) *Tray {
	return &Tray{
		cb:             cb,
		mode:           initialMode,
		overlayVisible: true,
		state:          initialState,
		iconData:       iconData,
		readyCh:        make(chan struct{}),
	}
}

// Ready returns a channel that closes when the tray is initialized.
func (t *Tray) Ready() <-chan struct{} {
	return t.readyCh
}

// Run starts the tray on the current thread. Blocks until quit.
func (t *Tray) Run() {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	hInstance, _, _ := procGetModuleHandleW.Call(0)

	// Create icon from BMP ICO data.
	if len(t.iconData) > 22 {
		bmpData := t.iconData[22:]
		h, _, _ := procCreateIconFromResourceEx.Call(
			uintptr(unsafe.Pointer(&bmpData[0])),
			uintptr(len(bmpData)),
			1, 0x00030000, 32, 32, LR_DEFAULTCOLOR,
		)
		if h != 0 {
			t.hIcon = h
		} else {
			log.Println("tray: failed to create icon handle")
		}
	}

	// Register window class.
	className, _ := syscall.UTF16PtrFromString("LiveTranslatorTray")
	wndProc := syscall.NewCallback(t.wndProc)
	wcx := wndClassEx{
		CbSize:        uint32(unsafe.Sizeof(wndClassEx{})),
		LpfnWndProc:   wndProc,
		HInstance:     hInstance,
		LpszClassName: className,
	}
	procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wcx)))

	// Create message-only window.
	windowName, _ := syscall.UTF16PtrFromString("LiveTranslatorTrayWnd")
	hwnd, _, _ := procCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(windowName)),
		0, 0, 0, 0, 0,
		uintptr(0xFFFFFFFFFFFFFFFD), // HWND_MESSAGE
		0, hInstance, 0,
	)
	if hwnd == 0 {
		log.Println("tray: failed to create window")
		close(t.readyCh)
		return
	}
	t.hwnd = hwnd

	// Add tray icon.
	nid := notifyIconData{
		CbSize:           uint32(unsafe.Sizeof(notifyIconData{})),
		HWnd:             hwnd,
		UID:              1,
		UFlags:           NIF_MESSAGE | NIF_ICON | NIF_TIP,
		UCallbackMessage: WM_TRAYICON,
		HIcon:            t.hIcon,
	}
	copy(nid.SzTip[:], utf16("LiveTranslator — Ready"))

	ret, _, _ := procShellNotifyIconW.Call(NIM_ADD, uintptr(unsafe.Pointer(&nid)))
	if ret != 0 {
		t.iconAdded = true
		log.Println("tray: icon added to system tray")
	} else {
		log.Println("tray: failed to add icon")
	}

	close(t.readyCh)

	// Message loop.
	var m msgT
	for {
		ret, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if ret == 0 || int32(ret) == -1 {
			break
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}

	// Cleanup.
	if t.iconAdded {
		t.removeIcon()
	}
	if t.hIcon != 0 {
		procDestroyIcon.Call(t.hIcon)
	}
}

// Quit sends a quit message to the tray thread.
func (t *Tray) Quit() {
	if t.hwnd != 0 {
		procPostMessageW.Call(t.hwnd, WM_QUIT_TRAY, 0, 0)
	}
}

// Notify shows a balloon notification from the tray icon.
func (t *Tray) Notify(title, message string) {
	log.Printf("[%s] %s", title, message)
	t.mu.Lock()
	t.balloons = append(t.balloons, balloonMsg{title, message})
	t.mu.Unlock()
	if t.hwnd != 0 {
		procPostMessageW.Call(t.hwnd, WM_SHOW_BALLOON, 0, 0)
	}
}

// SetRunning updates the running state in the tray.
func (t *Tray) SetRunning(running bool) {
	t.mu.Lock()
	t.running = running
	t.mu.Unlock()
	t.updateTooltip(running)
}

// SetOverlayVisible updates the overlay state in the tray.
func (t *Tray) SetOverlayVisible(visible bool) {
	t.mu.Lock()
	t.overlayVisible = visible
	t.mu.Unlock()
}

// UpdateState refreshes the status info.
func (t *Tray) UpdateState(s State) {
	t.mu.Lock()
	t.state = s
	t.mu.Unlock()
}

func (t *Tray) wndProc(hwnd uintptr, umsg uint32, wParam, lParam uintptr) uintptr {
	switch umsg {
	case WM_TRAYICON:
		switch lParam {
		case WM_RBUTTONUP:
			t.showContextMenu()
		case WM_LBUTTONDBLCLK:
			if t.cb.OnOpen != nil {
				t.cb.OnOpen()
			}
		}
		return 0

	case WM_COMMAND:
		t.handleMenuCommand(int(wParam & 0xFFFF))
		return 0

	case WM_SHOW_BALLOON:
		t.mu.Lock()
		if len(t.balloons) > 0 {
			b := t.balloons[0]
			t.balloons = t.balloons[1:]
			t.mu.Unlock()
			t.showBalloon(b.title, b.message)
		} else {
			t.mu.Unlock()
		}
		return 0

	case WM_QUIT_TRAY:
		procPostQuitMessage.Call(0)
		return 0
	}

	ret, _, _ := procDefWindowProcW.Call(hwnd, uintptr(umsg), wParam, lParam)
	return ret
}

func (t *Tray) showContextMenu() {
	hMenu, _, _ := procCreatePopupMenu.Call()
	if hMenu == 0 {
		return
	}
	defer procDestroyMenu.Call(hMenu)

	t.mu.Lock()
	running := t.running
	overlayVis := t.overlayVisible
	mode := t.mode
	state := t.state
	t.mu.Unlock()

	pos := uint32(0)

	// Header.
	addMenuItem(hMenu, pos, IDM_HEADER, "LiveTranslator", MFS_DISABLED)
	pos++
	addSeparator(hMenu, pos)
	pos++

	// Status lines.
	capStatus := "Stopped"
	if running {
		capStatus = "Running"
	}
	addMenuItem(hMenu, pos, IDM_STATUS_CAP, fmt.Sprintf("  Capture: %s", capStatus), MFS_DISABLED)
	pos++

	ovlStatus := "Visible"
	if !overlayVis {
		ovlStatus = "Hidden"
	}
	addMenuItem(hMenu, pos, IDM_STATUS_OVL, fmt.Sprintf("  Overlay: %s", ovlStatus), MFS_DISABLED)
	pos++

	modeLabel := "Transcribe"
	if mode == "translate" {
		modeLabel = "Translate"
	}
	addMenuItem(hMenu, pos, IDM_STATUS_MODE, fmt.Sprintf("  Mode: %s", modeLabel), MFS_DISABLED)
	pos++

	devLabel := state.DeviceName
	if devLabel == "" {
		devLabel = "Default"
	}
	addMenuItem(hMenu, pos, IDM_STATUS_DEV, fmt.Sprintf("  Device: %s", devLabel), MFS_DISABLED)
	pos++

	addMenuItem(hMenu, pos, IDM_STATUS_API, fmt.Sprintf("  API: %s", state.WhisperURL), MFS_DISABLED)
	pos++

	langLabel := state.Language
	if langLabel == "" {
		langLabel = "Auto-detect"
	}
	addMenuItem(hMenu, pos, IDM_STATUS_LANG, fmt.Sprintf("  Language: %s", langLabel), MFS_DISABLED)
	pos++

	addMenuItem(hMenu, pos, IDM_STATUS_HK, fmt.Sprintf("  Hotkey: %s", state.Hotkey), MFS_DISABLED)
	pos++

	addSeparator(hMenu, pos)
	pos++

	// Actions.
	if running {
		addMenuItem(hMenu, pos, IDM_START_STOP, "Stop Capture", MFS_ENABLED)
	} else {
		addMenuItem(hMenu, pos, IDM_START_STOP, "Start Capture", MFS_ENABLED)
	}
	pos++

	if overlayVis {
		addMenuItem(hMenu, pos, IDM_TOGGLE_OVL, "Hide Overlay", MFS_ENABLED)
	} else {
		addMenuItem(hMenu, pos, IDM_TOGGLE_OVL, "Show Overlay", MFS_ENABLED)
	}
	pos++

	addSeparator(hMenu, pos)
	pos++

	// Mode.
	transcribeState := uint32(MFS_ENABLED)
	translateState := uint32(MFS_ENABLED)
	if mode == "transcribe" {
		transcribeState |= MFS_CHECKED
	} else {
		translateState |= MFS_CHECKED
	}
	addMenuItem(hMenu, pos, IDM_TRANSCRIBE, "Transcribe", transcribeState)
	pos++
	addMenuItem(hMenu, pos, IDM_TRANSLATE, "Translate to English", translateState)
	pos++

	addSeparator(hMenu, pos)
	pos++

	addMenuItem(hMenu, pos, IDM_SETTINGS, "Open...", MFS_ENABLED)
	pos++
	addMenuItem(hMenu, pos, IDM_QUIT, "Quit", MFS_ENABLED)

	// Show menu at cursor.
	var pt point
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	procSetForegroundWindow.Call(t.hwnd)
	procTrackPopupMenuEx.Call(hMenu, TPM_RIGHTBUTTON|TPM_BOTTOMALIGN,
		uintptr(pt.X), uintptr(pt.Y), t.hwnd, 0)
}

func (t *Tray) handleMenuCommand(id int) {
	switch id {
	case IDM_START_STOP:
		t.mu.Lock()
		t.running = !t.running
		running := t.running
		t.mu.Unlock()
		t.updateTooltip(running)
		if t.cb.OnStartStop != nil {
			t.cb.OnStartStop(running)
		}

	case IDM_TOGGLE_OVL:
		if t.cb.OnToggleOverlay != nil {
			t.cb.OnToggleOverlay()
		}

	case IDM_TRANSCRIBE:
		t.mu.Lock()
		t.mode = "transcribe"
		t.mu.Unlock()
		if t.cb.OnModeChange != nil {
			t.cb.OnModeChange("transcribe")
		}

	case IDM_TRANSLATE:
		t.mu.Lock()
		t.mode = "translate"
		t.mu.Unlock()
		if t.cb.OnModeChange != nil {
			t.cb.OnModeChange("translate")
		}

	case IDM_SETTINGS:
		if t.cb.OnOpen != nil {
			t.cb.OnOpen()
		}

	case IDM_QUIT:
		if t.cb.OnQuit != nil {
			t.cb.OnQuit()
		}
		procPostQuitMessage.Call(0)
	}
}

func (t *Tray) updateTooltip(running bool) {
	status := "Stopped"
	if running {
		status = "Capturing"
	}
	nid := notifyIconData{
		CbSize: uint32(unsafe.Sizeof(notifyIconData{})),
		HWnd:   t.hwnd,
		UID:    1,
		UFlags: NIF_TIP,
	}
	copy(nid.SzTip[:], utf16(fmt.Sprintf("LiveTranslator — %s", status)))
	procShellNotifyIconW.Call(NIM_MODIFY, uintptr(unsafe.Pointer(&nid)))
}

func (t *Tray) showBalloon(title, message string) {
	if !t.iconAdded {
		return
	}
	nid := notifyIconData{
		CbSize:      uint32(unsafe.Sizeof(notifyIconData{})),
		HWnd:        t.hwnd,
		UID:         1,
		UFlags:      NIF_INFO,
		DwInfoFlags: NIIF_INFO,
	}
	copy(nid.SzInfoTitle[:], utf16(title))
	copy(nid.SzInfo[:], utf16(message))
	procShellNotifyIconW.Call(NIM_MODIFY, uintptr(unsafe.Pointer(&nid)))
}

func (t *Tray) removeIcon() {
	nid := notifyIconData{
		CbSize: uint32(unsafe.Sizeof(notifyIconData{})),
		HWnd:   t.hwnd,
		UID:    1,
	}
	procShellNotifyIconW.Call(NIM_DELETE, uintptr(unsafe.Pointer(&nid)))
	t.iconAdded = false
}

func addMenuItem(hMenu uintptr, pos uint32, id int, text string, state uint32) {
	txt, _ := syscall.UTF16PtrFromString(text)
	mii := menuItemInfo{
		CbSize:     uint32(unsafe.Sizeof(menuItemInfo{})),
		FMask:      MIIM_FTYPE | MIIM_STRING | MIIM_ID | MIIM_STATE,
		FType:      MFT_STRING,
		FState:     state,
		WID:        uint32(id),
		DwTypeData: txt,
		Cch:        uint32(len(text)),
	}
	procInsertMenuItemW.Call(hMenu, uintptr(pos), 1, uintptr(unsafe.Pointer(&mii)))
}

func addSeparator(hMenu uintptr, pos uint32) {
	mii := menuItemInfo{
		CbSize: uint32(unsafe.Sizeof(menuItemInfo{})),
		FMask:  MIIM_FTYPE,
		FType:  MFT_SEPARATOR,
	}
	procInsertMenuItemW.Call(hMenu, uintptr(pos), 1, uintptr(unsafe.Pointer(&mii)))
}

func utf16(s string) []uint16 {
	p, _ := syscall.UTF16FromString(s)
	return p
}
