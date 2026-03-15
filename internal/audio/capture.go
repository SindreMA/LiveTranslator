package audio

import (
	"fmt"
	"runtime"
	"time"
	"unsafe"

	ole "github.com/go-ole/go-ole"
	"github.com/moutend/go-wca/pkg/wca"
)

// CaptureResult holds a chunk of raw float32 samples plus format info.
type CaptureResult struct {
	Samples []float32
	Format  AudioFormat
}

// StartCapture begins WASAPI loopback capture on the given device (empty = default).
// It sends raw float32 PCM chunks on the output channel.
// Cancel by closing the stop channel.
func StartCapture(deviceID string, out chan<- CaptureResult, stop <-chan struct{}) error {
	errCh := make(chan error, 1)

	go func() {
		// WASAPI COM must run on a dedicated OS thread.
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()

		if err := ole.CoInitializeEx(0, ole.COINIT_APARTMENTTHREADED); err != nil {
			errCh <- fmt.Errorf("CoInitializeEx: %w", err)
			return
		}
		defer ole.CoUninitialize()

		var enumerator *wca.IMMDeviceEnumerator
		if err := wca.CoCreateInstance(
			wca.CLSID_MMDeviceEnumerator,
			0,
			wca.CLSCTX_ALL,
			wca.IID_IMMDeviceEnumerator,
			&enumerator,
		); err != nil {
			errCh <- fmt.Errorf("create enumerator: %w", err)
			return
		}
		defer enumerator.Release()

		device, err := getDevice(enumerator, deviceID)
		if err != nil {
			errCh <- err
			return
		}
		defer device.Release()

		var audioClient *wca.IAudioClient
		if err := device.Activate(wca.IID_IAudioClient, wca.CLSCTX_ALL, nil, &audioClient); err != nil {
			errCh <- fmt.Errorf("activate audio client: %w", err)
			return
		}
		defer audioClient.Release()

		var wfx *wca.WAVEFORMATEX
		if err := audioClient.GetMixFormat(&wfx); err != nil {
			errCh <- fmt.Errorf("get mix format: %w", err)
			return
		}
		defer ole.CoTaskMemFree(uintptr(unsafe.Pointer(wfx)))

		format := parseWaveFormat(wfx)

		// Initialize in shared mode with loopback flag, timer-driven.
		// Use 200ms buffer (in 100-nanosecond units).
		var defaultPeriod, minPeriod wca.REFERENCE_TIME
		if err := audioClient.GetDevicePeriod(&defaultPeriod, &minPeriod); err != nil {
			errCh <- fmt.Errorf("get device period: %w", err)
			return
		}

		if err := audioClient.Initialize(
			wca.AUDCLNT_SHAREMODE_SHARED,
			wca.AUDCLNT_STREAMFLAGS_LOOPBACK,
			defaultPeriod,
			0,
			wfx,
			nil,
		); err != nil {
			errCh <- fmt.Errorf("initialize audio client: %w", err)
			return
		}

		var captureClient *wca.IAudioCaptureClient
		if err := audioClient.GetService(wca.IID_IAudioCaptureClient, &captureClient); err != nil {
			errCh <- fmt.Errorf("get capture client: %w", err)
			return
		}
		defer captureClient.Release()

		if err := audioClient.Start(); err != nil {
			errCh <- fmt.Errorf("start audio client: %w", err)
			return
		}
		defer audioClient.Stop()

		// Signal that startup succeeded.
		errCh <- nil

		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				samples := drainBuffer(captureClient, format)
				if len(samples) > 0 {
					select {
					case out <- CaptureResult{Samples: samples, Format: format}:
					case <-stop:
						return
					}
				}
			}
		}
	}()

	// Wait for startup result.
	if err := <-errCh; err != nil {
		return err
	}
	return nil
}

func drainBuffer(client *wca.IAudioCaptureClient, format AudioFormat) []float32 {
	var allSamples []float32

	for {
		var packetSize uint32
		if err := client.GetNextPacketSize(&packetSize); err != nil || packetSize == 0 {
			break
		}

		var data *byte
		var frames uint32
		var flags uint32
		if err := client.GetBuffer(&data, &frames, &flags, nil, nil); err != nil {
			break
		}

		if frames > 0 && data != nil {
			byteCount := int(frames) * int(format.BlockAlign)
			buf := unsafe.Slice(data, byteCount)
			samples := float32SamplesFromBytes(buf, format)
			if flags&uint32(wca.AUDCLNT_BUFFERFLAGS_SILENT) != 0 {
				// Silent buffer — append zeros so chunker can detect silence.
				samples = make([]float32, int(frames)*int(format.Channels))
			}
			allSamples = append(allSamples, samples...)
		}

		client.ReleaseBuffer(frames)
	}

	return allSamples
}
