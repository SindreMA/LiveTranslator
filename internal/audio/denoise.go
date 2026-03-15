package audio

import (
	"math"
	"sync/atomic"
)

// noiseReductionLevel is the current noise reduction level (0-100).
// 0 = off, 100 = maximum isolation. Updated atomically from config.
var noiseReductionLevel atomic.Int32

// SetNoiseReduction updates the noise reduction level (0-100).
func SetNoiseReduction(level int) {
	if level < 0 {
		level = 0
	} else if level > 100 {
		level = 100
	}
	noiseReductionLevel.Store(int32(level))
}

// --- Biquad IIR filter (second-order section) ---

type biquad struct {
	b0, b1, b2 float64
	a1, a2     float64
	// state
	x1, x2 float64
	y1, y2 float64
}

func (bq *biquad) process(x float64) float64 {
	y := bq.b0*x + bq.b1*bq.x1 + bq.b2*bq.x2 - bq.a1*bq.y1 - bq.a2*bq.y2
	bq.x2 = bq.x1
	bq.x1 = x
	bq.y2 = bq.y1
	bq.y1 = y
	return y
}

// newHighPass creates a 2nd-order Butterworth high-pass filter.
func newHighPass(sampleRate, cutoffHz float64) *biquad {
	w0 := 2.0 * math.Pi * cutoffHz / sampleRate
	alpha := math.Sin(w0) / (2.0 * math.Sqrt(2.0)) // Q = sqrt(2)/2 for Butterworth
	cosw0 := math.Cos(w0)

	b0 := (1.0 + cosw0) / 2.0
	b1 := -(1.0 + cosw0)
	b2 := (1.0 + cosw0) / 2.0
	a0 := 1.0 + alpha
	a1 := -2.0 * cosw0
	a2 := 1.0 - alpha

	return &biquad{
		b0: b0 / a0, b1: b1 / a0, b2: b2 / a0,
		a1: a1 / a0, a2: a2 / a0,
	}
}

// newLowPass creates a 2nd-order Butterworth low-pass filter.
func newLowPass(sampleRate, cutoffHz float64) *biquad {
	w0 := 2.0 * math.Pi * cutoffHz / sampleRate
	alpha := math.Sin(w0) / (2.0 * math.Sqrt(2.0))
	cosw0 := math.Cos(w0)

	b0 := (1.0 - cosw0) / 2.0
	b1 := 1.0 - cosw0
	b2 := (1.0 - cosw0) / 2.0
	a0 := 1.0 + alpha
	a1 := -2.0 * cosw0
	a2 := 1.0 - alpha

	return &biquad{
		b0: b0 / a0, b1: b1 / a0, b2: b2 / a0,
		a1: a1 / a0, a2: a2 / a0,
	}
}

// --- Noise gate ---

type noiseGate struct {
	threshold    float64 // linear amplitude threshold
	attackCoeff  float64
	releaseCoeff float64
	holdSamples  int
	envAttack    float64
	envRelease   float64

	envelope    float64
	gain        float64
	holdCounter int
}

func newNoiseGate(sampleRate float64, thresholdDB float64) *noiseGate {
	threshold := math.Pow(10.0, thresholdDB/20.0)
	attackMs := 5.0
	releaseMs := 80.0
	holdMs := 50.0
	envAttackMs := 2.0
	envReleaseMs := 20.0
	return &noiseGate{
		threshold:    threshold,
		attackCoeff:  1.0 - math.Exp(-1.0/(sampleRate*attackMs/1000.0)),
		releaseCoeff: 1.0 - math.Exp(-1.0/(sampleRate*releaseMs/1000.0)),
		holdSamples:  int(sampleRate * holdMs / 1000.0),
		envAttack:    1.0 - math.Exp(-1.0/(sampleRate*envAttackMs/1000.0)),
		envRelease:   1.0 - math.Exp(-1.0/(sampleRate*envReleaseMs/1000.0)),
		gain:         0.0,
	}
}

func (ng *noiseGate) process(x float64) float64 {
	// Envelope follower (smoothed absolute value).
	abs := math.Abs(x)
	if abs > ng.envelope {
		ng.envelope += ng.envAttack * (abs - ng.envelope)
	} else {
		ng.envelope += ng.envRelease * (abs - ng.envelope)
	}

	// Gate logic.
	if ng.envelope >= ng.threshold {
		ng.holdCounter = ng.holdSamples
		ng.gain += ng.attackCoeff * (1.0 - ng.gain)
	} else if ng.holdCounter > 0 {
		ng.holdCounter--
	} else {
		ng.gain += ng.releaseCoeff * (0.0 - ng.gain)
	}

	return x * ng.gain
}

// --- Public processing function ---

// processVoiceIsolation applies bandpass filtering (voice freq isolation)
// and a noise gate to mono float32 samples at the given sample rate.
// The level parameter (0-100) controls aggressiveness:
//   - 0: no processing (passthrough)
//   - 50: moderate filtering (HP 80Hz, LP 7.5kHz, gate -30dB)
//   - 100: aggressive filtering (HP 200Hz, LP 4kHz, gate -20dB)
func processVoiceIsolation(samples []float32, sampleRate uint32) []float32 {
	level := int(noiseReductionLevel.Load())
	if level <= 0 {
		return samples
	}

	t := float64(level) / 100.0 // 0.0 to 1.0
	sr := float64(sampleRate)

	// Scale filter cutoffs based on level.
	// HP: 60Hz at level 1 → 300Hz at level 100
	hpCutoff := 60.0 + t*240.0
	// LP: 8kHz at level 1 → 3.5kHz at level 100
	lpCutoff := 8000.0 - t*4500.0

	// Gate threshold: -40dB at level 1 → -16dB at level 100
	gateDB := -40.0 + t*24.0

	hp := newHighPass(sr, hpCutoff)
	lp := newLowPass(sr, lpCutoff)
	gate := newNoiseGate(sr, gateDB)

	out := make([]float32, len(samples))
	for i, s := range samples {
		x := float64(s)
		x = hp.process(x)
		x = lp.process(x)
		x = gate.process(x)
		out[i] = float32(x)
	}
	return out
}
