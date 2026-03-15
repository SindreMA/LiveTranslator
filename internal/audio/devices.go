package audio

import (
	"fmt"
	"unsafe"

	ole "github.com/go-ole/go-ole"
	"github.com/moutend/go-wca/pkg/wca"
)

type Device struct {
	ID   string
	Name string
}

func ListRenderDevices() ([]Device, error) {
	err := ole.CoInitializeEx(0, ole.COINIT_APARTMENTTHREADED)
	if err != nil {
		// S_FALSE (0x00000001) means COM is already initialized on this thread — that's fine.
		const sFalse = 1
		if oleErr, ok := err.(*ole.OleError); !ok || oleErr.Code() != sFalse {
			return nil, fmt.Errorf("CoInitializeEx: %w", err)
		}
	} else {
		// Only uninitialize if we were the ones who initialized it.
		defer ole.CoUninitialize()
	}

	var enumerator *wca.IMMDeviceEnumerator
	if err := wca.CoCreateInstance(
		wca.CLSID_MMDeviceEnumerator,
		0,
		wca.CLSCTX_ALL,
		wca.IID_IMMDeviceEnumerator,
		&enumerator,
	); err != nil {
		return nil, fmt.Errorf("create device enumerator: %w", err)
	}
	defer enumerator.Release()

	var collection *wca.IMMDeviceCollection
	if err := enumerator.EnumAudioEndpoints(wca.ERender, wca.DEVICE_STATE_ACTIVE, &collection); err != nil {
		return nil, fmt.Errorf("enum audio endpoints: %w", err)
	}
	defer collection.Release()

	var count uint32
	if err := collection.GetCount(&count); err != nil {
		return nil, fmt.Errorf("get device count: %w", err)
	}

	devices := make([]Device, 0, count)
	for i := uint32(0); i < count; i++ {
		var device *wca.IMMDevice
		if err := collection.Item(i, &device); err != nil {
			continue
		}

		var id string
		if err := device.GetId(&id); err != nil {
			device.Release()
			continue
		}

		name := getDeviceName(device)

		devices = append(devices, Device{
			ID:   id,
			Name: name,
		})
		device.Release()
	}

	return devices, nil
}

func getDeviceName(device *wca.IMMDevice) string {
	var store *wca.IPropertyStore
	if err := device.OpenPropertyStore(wca.STGM_READ, &store); err != nil {
		return "Unknown Device"
	}
	defer store.Release()

	var pv wca.PROPVARIANT
	if err := store.GetValue(&wca.PKEY_Device_FriendlyName, &pv); err != nil {
		return "Unknown Device"
	}

	name := pv.String()
	if name == "" {
		return "Unknown Device"
	}
	return name
}

// AudioFormat holds the capture format details.
type AudioFormat struct {
	SampleRate   uint32
	BitsPerSample uint16
	Channels     uint16
	BlockAlign   uint16
}

func getDevice(enumerator *wca.IMMDeviceEnumerator, deviceID string) (*wca.IMMDevice, error) {
	if deviceID == "" {
		var device *wca.IMMDevice
		if err := enumerator.GetDefaultAudioEndpoint(wca.ERender, wca.EConsole, &device); err != nil {
			return nil, fmt.Errorf("get default audio endpoint: %w", err)
		}
		return device, nil
	}

	// GetDevice is not implemented in go-wca v0.3.0, so we enumerate
	// all render endpoints and find the one matching the requested ID.
	var collection *wca.IMMDeviceCollection
	if err := enumerator.EnumAudioEndpoints(wca.ERender, wca.DEVICE_STATE_ACTIVE, &collection); err != nil {
		return nil, fmt.Errorf("enum audio endpoints: %w", err)
	}
	defer collection.Release()

	var count uint32
	if err := collection.GetCount(&count); err != nil {
		return nil, fmt.Errorf("get device count: %w", err)
	}

	for i := uint32(0); i < count; i++ {
		var device *wca.IMMDevice
		if err := collection.Item(i, &device); err != nil {
			continue
		}
		var id string
		if err := device.GetId(&id); err != nil {
			device.Release()
			continue
		}
		if id == deviceID {
			return device, nil
		}
		device.Release()
	}

	return nil, fmt.Errorf("device %s not found", deviceID)
}

func parseWaveFormat(wfx *wca.WAVEFORMATEX) AudioFormat {
	return AudioFormat{
		SampleRate:    uint32(wfx.NSamplesPerSec),
		BitsPerSample: wfx.WBitsPerSample,
		Channels:      wfx.NChannels,
		BlockAlign:    wfx.NBlockAlign,
	}
}

// float32SamplesFromBytes converts raw WASAPI buffer bytes (float32 LE) to float32 slice.
func float32SamplesFromBytes(data []byte, format AudioFormat) []float32 {
	if format.BitsPerSample != 32 {
		return nil
	}
	sampleCount := len(data) / 4
	samples := make([]float32, sampleCount)
	for i := 0; i < sampleCount; i++ {
		bits := uint32(data[i*4]) | uint32(data[i*4+1])<<8 | uint32(data[i*4+2])<<16 | uint32(data[i*4+3])<<24
		samples[i] = *(*float32)(unsafe.Pointer(&bits))
	}
	return samples
}
