package bot

import (
	"math"
)

// LowpassFilterState はローパスフィルターの状態を保持します
type LowpassFilterState struct {
	prevSample float64
}

// EchoFilterState はエコーフィルターの状態を保持します
type EchoFilterState struct {
	buffer []float64
	index  int
}

// HeliumFilterState はヘリウム声フィルターの状態を保持します
type HeliumFilterState struct {
	phase float64
}

// AudioEffectProcessor は音声エフェクト処理を管理します
type AudioEffectProcessor struct {
	// フィルター状態（ギルドごと）
	lowpassStates map[string]*LowpassFilterState
	echoStates    map[string]*EchoFilterState
	heliumStates  map[string]*HeliumFilterState
}

// NewAudioEffectProcessor は新しいAudioEffectProcessorを作成します
func NewAudioEffectProcessor() *AudioEffectProcessor {
	return &AudioEffectProcessor{
		lowpassStates: make(map[string]*LowpassFilterState),
		echoStates:    make(map[string]*EchoFilterState),
		heliumStates:  make(map[string]*HeliumFilterState),
	}
}

// ApplyEffect は指定されたエフェクトをPCMデータに適用します
func (p *AudioEffectProcessor) ApplyEffect(guildID string, effect AudioEffect, pcmData []int16) []int16 {
	switch effect {
	case EffectBitcrush:
		return p.applyBitcrush(pcmData)
	case EffectLowpass:
		return p.applyLowpass(guildID, pcmData)
	case EffectEcho:
		return p.applyEcho(guildID, pcmData)
	case EffectHelium:
		return p.applyHelium(guildID, pcmData)
	default:
		return pcmData
	}
}

// applyBitcrush はビットクラッシュエフェクトを適用します
func (p *AudioEffectProcessor) applyBitcrush(pcmData []int16) []int16 {
	result := make([]int16, len(pcmData))
	for i, sample := range pcmData {
		// ビット深度を下げる（8bit相当）
		crushed := int16((int32(sample) >> 8) << 8)
		result[i] = crushed
	}
	return result
}

// applyLowpass はローパスフィルターを適用します
func (p *AudioEffectProcessor) applyLowpass(guildID string, pcmData []int16) []int16 {
	state, exists := p.lowpassStates[guildID]
	if !exists {
		state = &LowpassFilterState{}
		p.lowpassStates[guildID] = state
	}

	result := make([]int16, len(pcmData))
	alpha := 0.1 // カットオフ周波数を決定する係数

	for i, sample := range pcmData {
		// 簡単なローパスフィルター: y[n] = α * x[n] + (1-α) * y[n-1]
		filtered := alpha*float64(sample) + (1-alpha)*state.prevSample
		state.prevSample = filtered
		result[i] = int16(filtered)
	}

	return result
}

// applyEcho はエコーエフェクトを適用します
func (p *AudioEffectProcessor) applyEcho(guildID string, pcmData []int16) []int16 {
	state, exists := p.echoStates[guildID]
	if !exists {
		// エコー遅延: 0.3秒 (48000Hz * 0.3 = 14400サンプル)
		bufferSize := int(OpusSampleRate * 0.3)
		state = &EchoFilterState{
			buffer: make([]float64, bufferSize),
			index:  0,
		}
		p.echoStates[guildID] = state
	}

	result := make([]int16, len(pcmData))
	echoGain := 0.3 // エコーの強度

	for i, sample := range pcmData {
		// 現在のサンプル + 遅延されたサンプル
		delayed := state.buffer[state.index]
		output := float64(sample) + echoGain*delayed

		// バッファに現在のサンプルを保存
		state.buffer[state.index] = float64(sample)
		state.index = (state.index + 1) % len(state.buffer)

		// クリッピング防止
		if output > 32767 {
			output = 32767
		} else if output < -32768 {
			output = -32768
		}

		result[i] = int16(output)
	}

	return result
}

// applyHelium はヘリウム声エフェクトを適用します
func (p *AudioEffectProcessor) applyHelium(guildID string, pcmData []int16) []int16 {
	state, exists := p.heliumStates[guildID]
	if !exists {
		state = &HeliumFilterState{phase: 0}
		p.heliumStates[guildID] = state
	}

	result := make([]int16, len(pcmData))
	pitchShift := 1.5 // ピッチを1.5倍に

	for i, sample := range pcmData {
		// 簡単なピッチシフト（リングモジュレーション）
		modulator := math.Sin(state.phase * pitchShift)
		shifted := float64(sample) * (0.5 + 0.5*modulator)

		state.phase += 2 * math.Pi / float64(OpusSampleRate)
		if state.phase > 2*math.Pi {
			state.phase -= 2 * math.Pi
		}

		result[i] = int16(shifted)
	}

	return result
}

// CleanupGuildResources は指定されたギルドのエフェクト状態をクリーンアップします
func (p *AudioEffectProcessor) CleanupGuildResources(guildID string) {
	delete(p.lowpassStates, guildID)
	delete(p.echoStates, guildID)
	delete(p.heliumStates, guildID)
}

// CleanupAllResources はすべてのエフェクト状態をクリーンアップします
func (p *AudioEffectProcessor) CleanupAllResources() {
	p.lowpassStates = make(map[string]*LowpassFilterState)
	p.echoStates = make(map[string]*EchoFilterState)
	p.heliumStates = make(map[string]*HeliumFilterState)
}
