package audio

import (
	"bytes"
	"encoding/binary"
	"math"
)

const (
	// Chunk timing — keep short for near-realtime feel.
	minChunkSeconds = 0.8
	maxChunkSeconds = 2.5

	// Silence detection — short gap triggers flush quickly.
	silenceThresholdRMS = 0.008
	silenceDurationMs   = 150
	targetSampleRate    = 16000

	// Speech detection — minimum energy to consider a chunk worth transcribing.
	// Chunks below this overall RMS are almost certainly not speech and would
	// cause Whisper to hallucinate sentences like "Thank you." from noise.
	speechMinRMS = 0.015

	// Minimum fraction of frames that must exceed the speech threshold
	// to consider the chunk as containing actual speech.
	speechMinActiveFraction = 0.05
)

// StartChunker reads CaptureResults and produces WAV-encoded byte slices
// suitable for sending to the Whisper API.
// It flushes on silence boundaries (after min window) or at max window.
func StartChunker(in <-chan CaptureResult, out chan<- []byte, stop <-chan struct{}) {
	go func() {
		var (
			buffer        []float32
			format        AudioFormat
			formatSet     bool
			silentSamples int
		)

		for {
			select {
			case <-stop:
				// Flush remaining buffer on stop.
				if len(buffer) > 0 && formatSet {
					if wav := encodeWAV(buffer, format); wav != nil {
						select {
						case out <- wav:
						default:
						}
					}
				}
				return

			case result, ok := <-in:
				if !ok {
					return
				}

				if !formatSet {
					format = result.Format
					formatSet = true
				}

				buffer = append(buffer, result.Samples...)

				samplesPerChannel := len(buffer) / int(format.Channels)
				duration := float64(samplesPerChannel) / float64(format.SampleRate)

				if duration < minChunkSeconds {
					continue
				}

				// Check for silence in the latest samples.
				silentSamples += countSilentSamples(result.Samples, format)
				silenceDuration := float64(silentSamples) / float64(format.SampleRate)

				shouldFlush := duration >= maxChunkSeconds ||
					(duration >= minChunkSeconds && silenceDuration >= float64(silenceDurationMs)/1000.0)

				if shouldFlush {
					// Skip chunks without enough speech energy (prevents Whisper hallucination).
					if hasSpeech(buffer, format) {
						wav := encodeWAV(buffer, format)
						if wav != nil {
							select {
							case out <- wav:
							case <-stop:
								return
							}
						}
					}
					buffer = buffer[:0]
					silentSamples = 0
				}
			}
		}
	}()
}

// hasSpeech returns true if the buffer contains enough speech-level energy
// to be worth sending to Whisper. This prevents hallucination on quiet/noise chunks.
func hasSpeech(samples []float32, format AudioFormat) bool {
	channels := int(format.Channels)
	if channels == 0 || len(samples) == 0 {
		return false
	}
	frameCount := len(samples) / channels

	// Check 1: overall RMS energy must exceed minimum.
	var sumSq float64
	activeFrames := 0
	for i := 0; i < frameCount; i++ {
		var frameSq float64
		for ch := 0; ch < channels; ch++ {
			s := float64(samples[i*channels+ch])
			frameSq += s * s
		}
		frameRMS := math.Sqrt(frameSq / float64(channels))
		sumSq += frameSq / float64(channels)
		if frameRMS >= speechMinRMS {
			activeFrames++
		}
	}

	overallRMS := math.Sqrt(sumSq / float64(frameCount))
	if overallRMS < speechMinRMS {
		return false
	}

	// Check 2: at least speechMinActiveFraction of frames must be above speech threshold.
	activeFraction := float64(activeFrames) / float64(frameCount)
	return activeFraction >= speechMinActiveFraction
}

// countSilentSamples returns the number of consecutive silent mono-equivalent samples
// at the end of the given samples slice.
func countSilentSamples(samples []float32, format AudioFormat) int {
	channels := int(format.Channels)
	if channels == 0 {
		return 0
	}

	frameCount := len(samples) / channels
	silent := 0

	for i := frameCount - 1; i >= 0; i-- {
		rms := float64(0)
		for ch := 0; ch < channels; ch++ {
			s := float64(samples[i*channels+ch])
			rms += s * s
		}
		rms = math.Sqrt(rms / float64(channels))

		if rms < silenceThresholdRMS {
			silent++
		} else {
			break
		}
	}

	return silent
}

// encodeWAV converts float32 interleaved samples to a 16kHz mono 16-bit WAV.
func encodeWAV(samples []float32, format AudioFormat) []byte {
	mono := downmixToMono(samples, format.Channels)
	mono = processVoiceIsolation(mono, format.SampleRate)
	resampled := resample(mono, format.SampleRate, targetSampleRate)

	if len(resampled) == 0 {
		return nil
	}

	// Convert float32 to int16.
	pcm16 := make([]int16, len(resampled))
	for i, s := range resampled {
		// Clamp to [-1, 1].
		if s > 1.0 {
			s = 1.0
		} else if s < -1.0 {
			s = -1.0
		}
		pcm16[i] = int16(s * 32767)
	}

	// Build WAV file.
	var buf bytes.Buffer
	dataSize := len(pcm16) * 2
	fileSize := 36 + dataSize

	// RIFF header.
	buf.WriteString("RIFF")
	binary.Write(&buf, binary.LittleEndian, uint32(fileSize))
	buf.WriteString("WAVE")

	// fmt subchunk.
	buf.WriteString("fmt ")
	binary.Write(&buf, binary.LittleEndian, uint32(16))        // subchunk size
	binary.Write(&buf, binary.LittleEndian, uint16(1))         // PCM format
	binary.Write(&buf, binary.LittleEndian, uint16(1))         // mono
	binary.Write(&buf, binary.LittleEndian, uint32(targetSampleRate))
	binary.Write(&buf, binary.LittleEndian, uint32(targetSampleRate*2)) // byte rate
	binary.Write(&buf, binary.LittleEndian, uint16(2))         // block align
	binary.Write(&buf, binary.LittleEndian, uint16(16))        // bits per sample

	// data subchunk.
	buf.WriteString("data")
	binary.Write(&buf, binary.LittleEndian, uint32(dataSize))
	for _, s := range pcm16 {
		binary.Write(&buf, binary.LittleEndian, s)
	}

	return buf.Bytes()
}

func downmixToMono(samples []float32, channels uint16) []float32 {
	if channels <= 1 {
		return samples
	}

	ch := int(channels)
	frameCount := len(samples) / ch
	mono := make([]float32, frameCount)
	for i := 0; i < frameCount; i++ {
		var sum float32
		for c := 0; c < ch; c++ {
			sum += samples[i*ch+c]
		}
		mono[i] = sum / float32(ch)
	}
	return mono
}

// resample performs simple linear interpolation resampling.
func resample(samples []float32, fromRate, toRate uint32) []float32 {
	if fromRate == toRate {
		return samples
	}

	ratio := float64(fromRate) / float64(toRate)
	outLen := int(float64(len(samples)) / ratio)
	if outLen == 0 {
		return nil
	}

	out := make([]float32, outLen)
	for i := 0; i < outLen; i++ {
		srcIdx := float64(i) * ratio
		idx := int(srcIdx)
		frac := float32(srcIdx - float64(idx))

		if idx+1 < len(samples) {
			out[i] = samples[idx]*(1-frac) + samples[idx+1]*frac
		} else if idx < len(samples) {
			out[i] = samples[idx]
		}
	}
	return out
}
