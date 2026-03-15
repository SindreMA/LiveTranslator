package overlay

import (
	"math"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	gdi32    = windows.NewLazySystemDLL("gdi32.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")

	procRegisterClassExW    = user32.NewProc("RegisterClassExW")
	procCreateWindowExW     = user32.NewProc("CreateWindowExW")
	procShowWindow          = user32.NewProc("ShowWindow")
	procGetMessageW         = user32.NewProc("GetMessageW")
	procTranslateMessage    = user32.NewProc("TranslateMessage")
	procDispatchMessageW    = user32.NewProc("DispatchMessageW")
	procDefWindowProcW      = user32.NewProc("DefWindowProcW")
	procPostQuitMessage     = user32.NewProc("PostQuitMessage")
	procSetTimer            = user32.NewProc("SetTimer")
	procKillTimer           = user32.NewProc("KillTimer")
	procPostMessageW        = user32.NewProc("PostMessageW")
	procGetSystemMetrics    = user32.NewProc("GetSystemMetrics")
	procDestroyWindow       = user32.NewProc("DestroyWindow")
	procSetWindowPos        = user32.NewProc("SetWindowPos")
	procGetDC               = user32.NewProc("GetDC")
	procReleaseDC           = user32.NewProc("ReleaseDC")
	procUpdateLayeredWindow = user32.NewProc("UpdateLayeredWindow")
	procDrawTextW           = user32.NewProc("DrawTextW")

	procCreateFontW        = gdi32.NewProc("CreateFontW")
	procSelectObject       = gdi32.NewProc("SelectObject")
	procSetTextColor       = gdi32.NewProc("SetTextColor")
	procSetBkMode          = gdi32.NewProc("SetBkMode")
	procSetBkColor         = gdi32.NewProc("SetBkColor")
	procDeleteObject       = gdi32.NewProc("DeleteObject")
	procCreateCompatibleDC = gdi32.NewProc("CreateCompatibleDC")
	procCreateDIBSection   = gdi32.NewProc("CreateDIBSection")
	procDeleteDC           = gdi32.NewProc("DeleteDC")

	procGetModuleHandleW = kernel32.NewProc("GetModuleHandleW")
)

const (
	wsExLayered    = 0x00080000
	wsExTopmost    = 0x00000008
	wsExTransparent = 0x00000020
	wsExToolwindow  = 0x00000080
	wsExNoactivate  = 0x08000000
	wsPopup         = 0x80000000

	swShow = 5
	swHide = 0

	wmDestroy    = 0x0002
	wmTimer      = 0x0113
	wmUser       = 0x0400
	wmAppQuit    = wmUser + 4
	wmAppRepaint = wmUser + 5

	transparentBk    = 1
	opaqueBk         = 2
	clearTypeQuality = 5
	biRgb            = 0
	dibRgbColors     = 0
	ulwAlpha         = 0x00000002
	acSrcOver        = 0x00
	acSrcAlpha       = 0x01

	dtCenter    = 0x0001
	dtWordbreak = 0x0010
	dtCalcrect  = 0x0400
	dtNoprefix  = 0x0800

	timerAnimate = 1
	timerFade    = 2

	animIntervalMs = 16
	fadeTimeoutMs  = 5000

	smCxscreen = 0
	smCyscreen = 1
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

type msgT struct {
	HWnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      struct{ X, Y int32 }
}

type rect struct{ Left, Top, Right, Bottom int32 }
type pt struct{ X, Y int32 }
type sz struct{ CX, CY int32 }

type bitmapInfoHeader struct {
	BiSize          uint32
	BiWidth         int32
	BiHeight        int32
	BiPlanes        uint16
	BiBitCount      uint16
	BiCompression   uint32
	BiSizeImage     uint32
	BiXPelsPerMeter int32
	BiYPelsPerMeter int32
	BiClrUsed       uint32
	BiClrImportant  uint32
}

type bitmapInfo struct {
	BmiHeader bitmapInfoHeader
}

type blendFunc struct {
	BlendOp             byte
	BlendFlags          byte
	SourceConstantAlpha byte
	AlphaFormat         byte
}

type animLine struct {
	text    string
	yOffset float64
	alpha   float64
}

type Overlay struct {
	hwnd         uintptr
	mu           sync.Mutex
	lines        []animLine
	visible      bool
	fontSize     int
	opacity      int
	bgColor      uint32
	bgOpacity    int
	textOutline      bool
	outlineColor     uint32
	outlineThickness int
	width            int32
	height       int32
	screenW      int32
	screenH      int32
	animating    bool
}

var overlayInstance *Overlay

func New(fontSize, opacity int, bgColor uint32, bgOpacity int, textOutline bool, outlineColor uint32, outlineThickness int) *Overlay {
	if outlineThickness < 1 {
		outlineThickness = 1
	}
	o := &Overlay{
		fontSize: fontSize, opacity: opacity,
		bgColor: bgColor, bgOpacity: bgOpacity,
		textOutline: textOutline, outlineColor: outlineColor,
		outlineThickness: outlineThickness,
		visible: true,
	}
	overlayInstance = o
	return o
}

func (o *Overlay) Run(ready chan<- struct{}, stop <-chan struct{}) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	hInstance, _, _ := procGetModuleHandleW.Call(0)
	className, _ := syscall.UTF16PtrFromString("LiveTranslatorOverlay")
	wndProc := syscall.NewCallback(windowProc)

	wcx := wndClassEx{
		CbSize: uint32(unsafe.Sizeof(wndClassEx{})),
		LpfnWndProc: wndProc, HInstance: hInstance, LpszClassName: className,
	}
	procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wcx)))

	sw, _, _ := procGetSystemMetrics.Call(smCxscreen)
	sh, _, _ := procGetSystemMetrics.Call(smCyscreen)
	o.screenW = int32(sw)
	o.screenH = int32(sh)
	o.width = o.screenW * 3 / 4
	o.height = o.calcHeight()
	x := (o.screenW - o.width) / 2
	y := o.screenH - o.height - o.screenH/10

	windowName, _ := syscall.UTF16PtrFromString("LiveTranslator")
	exStyle := uintptr(wsExLayered | wsExTopmost | wsExTransparent | wsExToolwindow | wsExNoactivate)

	hwnd, _, err := procCreateWindowExW.Call(
		exStyle, uintptr(unsafe.Pointer(className)), uintptr(unsafe.Pointer(windowName)),
		wsPopup, uintptr(x), uintptr(y), uintptr(o.width), uintptr(o.height),
		0, 0, hInstance, 0,
	)
	if hwnd == 0 {
		return err
	}
	o.hwnd = hwnd
	o.updateLayered()
	procShowWindow.Call(hwnd, swShow)

	if ready != nil {
		close(ready)
	}

	go func() {
		<-stop
		procPostMessageW.Call(hwnd, wmAppQuit, 0, 0)
	}()

	var msg msgT
	for {
		ret, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if ret == 0 || int32(ret) == -1 {
			break
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&msg)))
	}
	return nil
}

func (o *Overlay) calcHeight() int32 {
	return int32(o.fontSize)*2 + 80
}

func (o *Overlay) resizeWindow() {
	if o.hwnd == 0 || o.screenW == 0 {
		return
	}
	o.height = o.calcHeight()
	x := (o.screenW - o.width) / 2
	y := o.screenH - o.height - o.screenH/10
	procSetWindowPos.Call(o.hwnd, 0, uintptr(x), uintptr(y), uintptr(o.width), uintptr(o.height), 0)
}

func (o *Overlay) SetText(text string) {
	o.mu.Lock()
	newLine := animLine{text: text, yOffset: 30, alpha: 0.0}
	if len(o.lines) >= 2 {
		o.lines = o.lines[1:]
	}
	for i := range o.lines {
		o.lines[i].yOffset = -8
	}
	o.lines = append(o.lines, newLine)
	if !o.animating {
		o.animating = true
		procSetTimer.Call(o.hwnd, timerAnimate, animIntervalMs, 0)
	}
	o.mu.Unlock()
	procKillTimer.Call(o.hwnd, timerFade)
	procSetTimer.Call(o.hwnd, timerFade, fadeTimeoutMs, 0)
	o.requestRepaint()
}

func (o *Overlay) Show()   { if o.hwnd != 0 { procShowWindow.Call(o.hwnd, swShow); o.mu.Lock(); o.visible = true; o.mu.Unlock() } }
func (o *Overlay) Hide()   { if o.hwnd != 0 { procShowWindow.Call(o.hwnd, swHide); o.mu.Lock(); o.visible = false; o.mu.Unlock() } }
func (o *Overlay) Hwnd() uintptr { return o.hwnd }

func (o *Overlay) Toggle() {
	o.mu.Lock()
	vis := o.visible
	o.mu.Unlock()
	if vis { o.Hide() } else { o.Show() }
}

func (o *Overlay) IsVisible() bool { o.mu.Lock(); defer o.mu.Unlock(); return o.visible }

func (o *Overlay) SetFontSize(s int)  { o.mu.Lock(); o.fontSize = s; o.resizeWindow(); o.mu.Unlock(); o.requestRepaint() }
func (o *Overlay) SetOpacity(v int)   { o.mu.Lock(); o.opacity = v; o.mu.Unlock(); o.requestRepaint() }
func (o *Overlay) SetBgColor(v uint32) { o.mu.Lock(); o.bgColor = v; o.mu.Unlock(); o.requestRepaint() }
func (o *Overlay) SetBgOpacity(v int) { o.mu.Lock(); o.bgOpacity = v; o.mu.Unlock(); o.requestRepaint() }
func (o *Overlay) SetTextOutline(e bool, c uint32, thickness int) {
	if thickness < 1 {
		thickness = 1
	}
	o.mu.Lock()
	o.textOutline = e
	o.outlineColor = c
	o.outlineThickness = thickness
	o.mu.Unlock()
	o.requestRepaint()
}

func (o *Overlay) requestRepaint() {
	if o.hwnd != 0 {
		procPostMessageW.Call(o.hwnd, wmAppRepaint, 0, 0)
	}
}

func FadeTimeoutMs() time.Duration { return fadeTimeoutMs * time.Millisecond }

func windowProc(hwnd uintptr, msg uint32, wParam, lParam uintptr) uintptr {
	switch msg {
	case wmTimer:
		if overlayInstance != nil {
			switch wParam {
			case timerAnimate:
				overlayInstance.tickAnimation()
			case timerFade:
				overlayInstance.startFadeOut()
			}
		}
		return 0
	case wmAppRepaint:
		if overlayInstance != nil {
			overlayInstance.updateLayered()
		}
		return 0
	case wmAppQuit:
		procDestroyWindow.Call(hwnd)
		return 0
	case wmDestroy:
		procPostQuitMessage.Call(0)
		return 0
	}
	ret, _, _ := procDefWindowProcW.Call(hwnd, uintptr(msg), wParam, lParam)
	return ret
}

func (o *Overlay) tickAnimation() {
	o.mu.Lock()
	allSettled := true
	for i := range o.lines {
		l := &o.lines[i]
		if l.yOffset > 0.5 || l.yOffset < -0.5 {
			l.yOffset *= 0.75
			allSettled = false
		} else {
			l.yOffset = 0
		}
		if l.alpha < 0.99 {
			l.alpha += (1.0 - l.alpha) * 0.2
			allSettled = false
		} else {
			l.alpha = 1.0
		}
	}
	kept := o.lines[:0]
	for _, l := range o.lines {
		if l.alpha > 0.01 || l.text != "" {
			kept = append(kept, l)
		}
	}
	o.lines = kept
	if allSettled {
		o.animating = false
		procKillTimer.Call(o.hwnd, timerAnimate)
	}
	o.mu.Unlock()
	o.updateLayered()
}

func (o *Overlay) startFadeOut() {
	o.mu.Lock()
	for i := range o.lines {
		o.lines[i].text = ""
	}
	if !o.animating {
		o.animating = true
		procSetTimer.Call(o.hwnd, timerAnimate, animIntervalMs, 0)
	}
	o.mu.Unlock()
	procKillTimer.Call(o.hwnd, timerFade)
	o.updateLayered()
}

// updateLayered renders overlay into a 32-bit BGRA bitmap with per-pixel alpha.
func (o *Overlay) updateLayered() {
	o.mu.Lock()
	lines := make([]animLine, len(o.lines))
	copy(lines, o.lines)
	fontSize := o.fontSize
	w := o.width
	h := o.height
	bgColor := o.bgColor
	bgAlpha := o.bgOpacity
	globalAlpha := o.opacity
	textOutline := o.textOutline
	outlineColor := o.outlineColor
	outlineThickness := o.outlineThickness
	o.mu.Unlock()

	if w <= 0 || h <= 0 {
		return
	}

	screenDC, _, _ := procGetDC.Call(0)
	defer procReleaseDC.Call(0, screenDC)

	// Main surface (what we'll display).
	mainDC, mainBmp, mainPix := createDIB(screenDC, w, h)
	if mainDC == 0 {
		return
	}
	defer procDeleteDC.Call(mainDC)
	defer procDeleteObject.Call(mainBmp)

	stride := int(w)
	pixelCount := stride * int(h)

	// Clear to transparent.
	for i := 0; i < pixelCount; i++ {
		mainPix[i] = [4]byte{0, 0, 0, 0}
	}

	// Filter visible lines.
	var visibleLines []animLine
	for _, l := range lines {
		if l.text != "" && l.alpha > 0.02 {
			visibleLines = append(visibleLines, l)
		}
	}

	if len(visibleLines) > 0 {
		// Create font.
		fontName, _ := syscall.UTF16PtrFromString("Segoe UI Semibold")
		hFont, _, _ := procCreateFontW.Call(
			uintptr(-fontSize), 0, 0, 0, 600, 0, 0, 0, 0, 0, 0,
			clearTypeQuality, 0, uintptr(unsafe.Pointer(fontName)),
		)
		defer procDeleteObject.Call(hFont)

		// Use a temporary DC for text measurement.
		tmpDC, tmpBmp, _ := createDIB(screenDC, w, h)
		defer procDeleteDC.Call(tmpDC)
		defer procDeleteObject.Call(tmpBmp)

		oldFont, _, _ := procSelectObject.Call(tmpDC, hFont)

		// Measure combined text.
		combined := ""
		for i, l := range visibleLines {
			if i > 0 {
				combined += "\n"
			}
			combined += l.text
		}
		textUTF16, _ := syscall.UTF16FromString(combined)
		calcR := rect{Left: 0, Top: 0, Right: w - 40, Bottom: h}
		procDrawTextW.Call(tmpDC, uintptr(unsafe.Pointer(&textUTF16[0])), uintptr(len(textUTF16)-1),
			uintptr(unsafe.Pointer(&calcR)), dtCenter|dtWordbreak|dtCalcrect|dtNoprefix)

		textW := calcR.Right - calcR.Left
		textH := calcR.Bottom - calcR.Top
		padX := int32(24)
		padY := int32(14)

		avgOff := float64(0)
		for _, l := range visibleLines {
			avgOff += l.yOffset
		}
		avgOff /= float64(len(visibleLines))

		pillW := textW + padX*2
		pillH := textH + padY*2
		pillX := (w - pillW) / 2
		pillY := (h-pillH)/2 + int32(avgOff)
		cornerR := float64(pillH) / 2
		if cornerR < 16 {
			cornerR = 16
		}

		// Draw pill background into pixel buffer.
		bgR := uint8(bgColor & 0xFF)
		bgG := uint8((bgColor >> 8) & 0xFF)
		bgB := uint8((bgColor >> 16) & 0xFF)
		bgA := clampByte(bgAlpha)

		for py := pillY; py < pillY+pillH && py < h; py++ {
			if py < 0 {
				continue
			}
			for px := pillX; px < pillX+pillW && px < w; px++ {
				if px < 0 {
					continue
				}
				if !inRoundedRect(float64(px), float64(py), float64(pillX), float64(pillY),
					float64(pillW), float64(pillH), cornerR) {
					continue
				}
				a := float64(bgA) / 255.0
				mainPix[int(py)*stride+int(px)] = [4]byte{
					uint8(float64(bgB) * a), uint8(float64(bgG) * a), uint8(float64(bgR) * a), bgA,
				}
			}
		}

		procSelectObject.Call(tmpDC, oldFont)

		// Render each text line using the two-surface technique:
		// 1. Draw white text on black surface → luminance = coverage
		// 2. Composite text color * coverage onto main buffer
		lineY := pillY + padY
		for _, l := range visibleLines {
			if l.text == "" {
				continue
			}
			lineUTF16, _ := syscall.UTF16FromString(l.text)

			// Measure line.
			oldF2, _, _ := procSelectObject.Call(tmpDC, hFont)
			lr := rect{Left: 0, Top: 0, Right: w - 40, Bottom: h}
			procDrawTextW.Call(tmpDC, uintptr(unsafe.Pointer(&lineUTF16[0])), uintptr(len(lineUTF16)-1),
				uintptr(unsafe.Pointer(&lr)), dtCenter|dtWordbreak|dtCalcrect|dtNoprefix)
			lineH := lr.Bottom - lr.Top
			procSelectObject.Call(tmpDC, oldF2)

			drawY := lineY + int32(l.yOffset)
			textAlpha := l.alpha

			// Text outline.
			if textOutline {
				olR := uint8(outlineColor & 0xFF)
				olG := uint8((outlineColor >> 8) & 0xFF)
				olB := uint8((outlineColor >> 16) & 0xFF)
				olA := uint8(200 * textAlpha)
				for d := int32(1); d <= int32(outlineThickness); d++ {
					offsets := [][2]int32{{-d, 0}, {d, 0}, {0, -d}, {0, d}}
					for _, off := range offsets {
						renderTextToBuffer(screenDC, w, h, hFont, lineUTF16,
							rect{Left: pillX + padX + off[0], Top: drawY + off[1],
								Right: pillX + padX + textW + off[0], Bottom: drawY + lineH + off[1]},
							olR, olG, olB, olA, mainPix, stride)
					}
				}
			}

			// Main text (white).
			tA := uint8(255 * textAlpha)
			renderTextToBuffer(screenDC, w, h, hFont, lineUTF16,
				rect{Left: pillX + padX, Top: drawY, Right: pillX + padX + textW, Bottom: drawY + lineH},
				255, 255, 255, tA, mainPix, stride)

			lineY += lineH + 4
		}
	}

	// UpdateLayeredWindow.
	ptSrc := pt{0, 0}
	s := sz{w, h}
	bf := blendFunc{
		BlendOp: acSrcOver, SourceConstantAlpha: clampByte(globalAlpha), AlphaFormat: acSrcAlpha,
	}
	procUpdateLayeredWindow.Call(o.hwnd, screenDC, 0, uintptr(unsafe.Pointer(&s)),
		mainDC, uintptr(unsafe.Pointer(&ptSrc)), 0, uintptr(unsafe.Pointer(&bf)), ulwAlpha)
}

// renderTextToBuffer draws text on a small temporary surface covering just the text rect,
// reads coverage, and composites colored text onto the main pixel buffer.
func renderTextToBuffer(screenDC uintptr, fullW, fullH int32, hFont uintptr, text []uint16,
	r rect, cR, cG, cB, cA uint8, dst [][4]byte, stride int) {

	if cA == 0 {
		return
	}

	// Clamp rect to surface bounds with margin for anti-aliasing.
	left := r.Left - 2
	top := r.Top - 2
	right := r.Right + 2
	bot := r.Bottom + 2
	if left < 0 { left = 0 }
	if top < 0 { top = 0 }
	if right > fullW { right = fullW }
	if bot > fullH { bot = fullH }
	tw := right - left
	th := bot - top
	if tw <= 0 || th <= 0 {
		return
	}

	// Create a small temp DIB just for the text region.
	tmpDC, tmpBmp, tmpPix := createDIB(screenDC, tw, th)
	if tmpDC == 0 {
		return
	}
	defer procDeleteDC.Call(tmpDC)
	defer procDeleteObject.Call(tmpBmp)

	// Already zeroed by CreateDIBSection. Draw white text offset to local coords.
	oldFont, _, _ := procSelectObject.Call(tmpDC, hFont)
	procSetBkMode.Call(tmpDC, transparentBk)
	procSetTextColor.Call(tmpDC, 0x00FFFFFF)
	localR := rect{Left: r.Left - left, Top: r.Top - top, Right: r.Right - left, Bottom: r.Bottom - top}
	procDrawTextW.Call(tmpDC, uintptr(unsafe.Pointer(&text[0])), uintptr(len(text)-1),
		uintptr(unsafe.Pointer(&localR)), dtCenter|dtWordbreak|dtNoprefix)
	procSelectObject.Call(tmpDC, oldFont)

	// Composite onto main buffer.
	tmpStride := int(tw)
	for py := int32(0); py < th; py++ {
		dstY := int(top + py)
		if dstY < 0 || dstY >= int(fullH) { continue }
		for px := int32(0); px < tw; px++ {
			dstX := int(left + px)
			if dstX < 0 || dstX >= int(fullW) { continue }

			tp := tmpPix[int(py)*tmpStride+int(px)]
			coverage := tp[0]
			if tp[1] > coverage { coverage = tp[1] }
			if tp[2] > coverage { coverage = tp[2] }
			if coverage == 0 { continue }

			a := uint16(coverage) * uint16(cA) / 255
			if a == 0 { continue }

			srcB := uint8(uint16(cB) * a / 255)
			srcG := uint8(uint16(cG) * a / 255)
			srcR := uint8(uint16(cR) * a / 255)
			srcA := uint8(a)

			dp := &dst[dstY*stride+dstX]
			invA := uint16(255 - srcA)
			dp[0] = srcB + uint8(uint16(dp[0])*invA/255)
			dp[1] = srcG + uint8(uint16(dp[1])*invA/255)
			dp[2] = srcR + uint8(uint16(dp[2])*invA/255)
			dp[3] = srcA + uint8(uint16(dp[3])*invA/255)
		}
	}
}

// createDIB creates a memory DC with a 32-bit top-down BGRA DIB section.
func createDIB(screenDC uintptr, w, h int32) (dc, bmp uintptr, pixels [][4]byte) {
	memDC, _, _ := procCreateCompatibleDC.Call(screenDC)
	if memDC == 0 {
		return 0, 0, nil
	}

	bi := bitmapInfo{BmiHeader: bitmapInfoHeader{
		BiSize: uint32(unsafe.Sizeof(bitmapInfoHeader{})),
		BiWidth: w, BiHeight: -h, BiPlanes: 1, BiBitCount: 32, BiCompression: biRgb,
	}}

	var bits unsafe.Pointer
	hBmp, _, _ := procCreateDIBSection.Call(memDC, uintptr(unsafe.Pointer(&bi)), dibRgbColors,
		uintptr(unsafe.Pointer(&bits)), 0, 0)
	if hBmp == 0 {
		procDeleteDC.Call(memDC)
		return 0, 0, nil
	}

	procSelectObject.Call(memDC, hBmp)
	pix := unsafe.Slice((*[4]byte)(bits), int(w)*int(h))
	return memDC, hBmp, pix
}

func inRoundedRect(px, py, rx, ry, rw, rh, radius float64) bool {
	if px < rx || px >= rx+rw || py < ry || py >= ry+rh {
		return false
	}
	r := math.Min(radius, math.Min(rw/2, rh/2))
	corners := [4][2]float64{
		{rx + r, ry + r}, {rx + rw - r, ry + r},
		{rx + r, ry + rh - r}, {rx + rw - r, ry + rh - r},
	}
	checks := [4]bool{
		px < rx+r && py < ry+r,
		px >= rx+rw-r && py < ry+r,
		px < rx+r && py >= ry+rh-r,
		px >= rx+rw-r && py >= ry+rh-r,
	}
	for i, check := range checks {
		if check {
			dx := px - corners[i][0]
			dy := py - corners[i][1]
			return dx*dx+dy*dy <= r*r
		}
	}
	return true
}

func clampByte(v int) uint8 {
	if v < 0 { return 0 }
	if v > 255 { return 255 }
	return uint8(v)
}
