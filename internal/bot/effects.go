package bot

import (
	"log"
	"sync"
)

// AudioEffectConfig は、オーディオエフェクトの設定を保持します
type AudioEffectConfig struct {
	Effect AudioEffect
}

// LowpassFilterState は、ローパスフィルターの状態を保持します
type LowpassFilterState struct {
	lastOutputLeft  float32
	lastOutputRight float32
}

// EchoFilterState は、エコーエフェクトの状態を保持します
type EchoFilterState struct {
	delayBufferLeft  []float32
	delayBufferRight []float32
	bufferIndex      int
	bufferSize       int
}

// HeliumFilterState は、ヘリウム声エフェクトの状態を保持します
type HeliumFilterState struct {
	pitchBufferLeft  []float32
	pitchBufferRight []float32
	readIndex        float32
	writeIndex       int
	bufferSize       int
	lastSampleLeft   float32
	lastSampleRight  float32
}

// AudioEffectProcessor は音声エフェクト処理を管理します
type AudioEffectProcessor struct {
	// 音声エフェクト設定
	audioEffects      map[string]AudioEffectConfig // guildID -> AudioEffectConfig
	audioEffectsMutex sync.Mutex

	// ローパスフィルターの状態（ギルドごと）
	lowpassStates      map[string]*LowpassFilterState // guildID -> フィルター状態
	lowpassStatesMutex sync.Mutex

	// エコーエフェクトの状態（ギルドごと）
	echoStates      map[string]*EchoFilterState // guildID -> エコー状態
	echoStatesMutex sync.Mutex

	// ヘリウム声エフェクトの状態（ギルドごと）
	heliumStates      map[string]*HeliumFilterState // guildID -> ヘリウム声状態
	heliumStatesMutex sync.Mutex
}

// NewAudioEffectProcessor は新しいAudioEffectProcessorを作成します
func NewAudioEffectProcessor() *AudioEffectProcessor {
	return &AudioEffectProcessor{
		audioEffects:  make(map[string]AudioEffectConfig),
		lowpassStates: make(map[string]*LowpassFilterState),
		echoStates:    make(map[string]*EchoFilterState),
		heliumStates:  make(map[string]*HeliumFilterState),
	}
}

// SetAudioEffect は、指定されたギルドのオーディオエフェクトを設定します
func (p *AudioEffectProcessor) SetAudioEffect(guildID string, effect AudioEffect) {
	p.audioEffectsMutex.Lock()
	p.audioEffects[guildID] = AudioEffectConfig{Effect: effect}
	p.audioEffectsMutex.Unlock()
	log.Printf("[AudioEffectProcessor] Set audio effect for guild %s: %v", guildID, effect)
}

// ApplyAudioEffect は、指定されたギルドの設定に基づいて音声エフェクトを適用します
func (p *AudioEffectProcessor) ApplyAudioEffect(guildID string, pcmData []int16) {
	// エフェクト設定を取得
	p.audioEffectsMutex.Lock()
	effectConfig, exists := p.audioEffects[guildID]
	p.audioEffectsMutex.Unlock()

	if !exists {
		return // エフェクト設定がない場合は何もしない
	}

	switch effectConfig.Effect {
	case EffectNone:
		// 何もしない
	case EffectBitcrush:
		p.applyBitcrushEffect(pcmData)
	case EffectLowpass:
		p.applyLowpassEffect(guildID, pcmData)
	case EffectEcho:
		p.applyEchoEffect(guildID, pcmData)
	case EffectHelium:
		p.applyHeliumEffect(guildID, pcmData)
	}
}

// CleanupGuildEffects は、指定されたギルドのエフェクト状態をクリーンアップします
func (p *AudioEffectProcessor) CleanupGuildEffects(guildID string) {
	p.audioEffectsMutex.Lock()
	delete(p.audioEffects, guildID)
	p.audioEffectsMutex.Unlock()

	p.lowpassStatesMutex.Lock()
	delete(p.lowpassStates, guildID)
	p.lowpassStatesMutex.Unlock()

	p.echoStatesMutex.Lock()
	delete(p.echoStates, guildID)
	p.echoStatesMutex.Unlock()

	p.heliumStatesMutex.Lock()
	delete(p.heliumStates, guildID)
	p.heliumStatesMutex.Unlock()
}

// CleanupAllEffects は、すべてのエフェクト状態をクリーンアップします
func (p *AudioEffectProcessor) CleanupAllEffects() {
	p.audioEffectsMutex.Lock()
	p.lowpassStatesMutex.Lock()
	p.echoStatesMutex.Lock()
	p.heliumStatesMutex.Lock()
	defer p.audioEffectsMutex.Unlock()
	defer p.lowpassStatesMutex.Unlock()
	defer p.echoStatesMutex.Unlock()
	defer p.heliumStatesMutex.Unlock()

	p.audioEffects = make(map[string]AudioEffectConfig)
	p.lowpassStates = make(map[string]*LowpassFilterState)
	p.echoStates = make(map[string]*EchoFilterState)
	p.heliumStates = make(map[string]*HeliumFilterState)
}

// applyBitcrushEffect は、ビットクラッシュエフェクトを適用します
func (p *AudioEffectProcessor) applyBitcrushEffect(pcmData []int16) {
	// より荒いビットクラッシュエフェクト
	const bitReduction = 8         // 16bitを8bitに下げる（8bit削る）
	const bitMask = ^int16(0x00FF) // 下位8bitをマスクするためのビット反転
	const downsampleRate = 4       // サンプルレート削減（4倍に間引き）
	const volumeReduction = 0.8    // 音量を60%に低下
	const distortionLevel = 1.3    // ディストーション強度

	for i := range pcmData {
		// 1. ビット深度削減（16bit → 8bit相当）
		sample := pcmData[i] & bitMask

		// 2. サンプルレート削減（ローファイ効果）
		if i%downsampleRate != 0 {
			// 間引きしたサンプルは前のサンプルの値を使用
			if i > 0 {
				sample = pcmData[i-1] & bitMask
			}
		}

		// 3. ディストーション（オーバードライブ）
		distorted := float32(sample) * distortionLevel
		if distorted > 32767 {
			distorted = 32767
		} else if distorted < -32768 {
			distorted = -32768
		}
		sample = int16(distorted)

		// 4. 音量削減
		pcmData[i] = int16(float32(sample) * volumeReduction)
	}
}

// applyHeliumEffect は、ヘリウム声エフェクトを適用します
func (p *AudioEffectProcessor) applyHeliumEffect(guildID string, pcmData []int16) {
	// ヘリウム声状態を取得または作成
	p.heliumStatesMutex.Lock()
	state, exists := p.heliumStates[guildID]
	if !exists {
		// ヘリウム声のパラメータ
		bufferSizeMs := 50  // 50ms のバッファサイズ
		sampleRate := 48000 // 48kHz
		bufferSize := (bufferSizeMs * sampleRate) / 1000

		state = &HeliumFilterState{
			pitchBufferLeft:  make([]float32, bufferSize),
			pitchBufferRight: make([]float32, bufferSize),
			readIndex:        0,
			writeIndex:       0,
			bufferSize:       bufferSize,
			lastSampleLeft:   0,
			lastSampleRight:  0,
		}
		p.heliumStates[guildID] = state
	}
	p.heliumStatesMutex.Unlock()

	// ヘリウム声のパラメータ
	const pitchShift = float32(1.7)         // ピッチを1.7倍に（ヘリウムガス効果）
	const brightness = float32(1.2)         // 音の明るさ（20%アップ）
	const volumeCompensation = float32(0.8) // 音量補正（80%）
	const highFreqBoost = float32(1.3)      // 高周波数ブースト（30%）

	// ステレオ（2チャンネル）のPCMデータに対してヘリウム声を適用
	for i := 0; i < len(pcmData); i += 2 {
		if i+1 < len(pcmData) {
			// 現在のサンプルを取得
			inputLeft := float32(pcmData[i])
			inputRight := float32(pcmData[i+1])

			// 1. 高周波数ブースト（ヘリウムの明るい音質）
			boostedLeft := inputLeft * highFreqBoost
			boostedRight := inputRight * highFreqBoost

			// 2. バッファに書き込み
			state.pitchBufferLeft[state.writeIndex] = boostedLeft
			state.pitchBufferRight[state.writeIndex] = boostedRight

			// 3. ピッチシフト（読み取りインデックスを進める速度を調整）
			readIndexInt := int(state.readIndex)
			readIndexFrac := state.readIndex - float32(readIndexInt)

			// 4. 線形補間でサンプルを取得
			var outputLeft, outputRight float32
			if readIndexInt < state.bufferSize {
				nextIndex := (readIndexInt + 1) % state.bufferSize

				// 線形補間
				outputLeft = state.pitchBufferLeft[readIndexInt]*(1-readIndexFrac) +
					state.pitchBufferLeft[nextIndex]*readIndexFrac
				outputRight = state.pitchBufferRight[readIndexInt]*(1-readIndexFrac) +
					state.pitchBufferRight[nextIndex]*readIndexFrac
			} else {
				outputLeft = state.lastSampleLeft
				outputRight = state.lastSampleRight
			}

			// 5. 明るさ調整
			outputLeft *= brightness
			outputRight *= brightness

			// 6. 音量補正
			outputLeft *= volumeCompensation
			outputRight *= volumeCompensation

			// 7. クリッピング防止
			if outputLeft > 32767 {
				outputLeft = 32767
			} else if outputLeft < -32768 {
				outputLeft = -32768
			}
			if outputRight > 32767 {
				outputRight = 32767
			} else if outputRight < -32768 {
				outputRight = -32768
			}

			pcmData[i] = int16(outputLeft)
			pcmData[i+1] = int16(outputRight)

			// 8. 前回サンプルを保存
			state.lastSampleLeft = outputLeft
			state.lastSampleRight = outputRight

			// 9. インデックスを進める
			state.readIndex += pitchShift
			for state.readIndex >= float32(state.bufferSize) {
				state.readIndex -= float32(state.bufferSize)
			}

			state.writeIndex = (state.writeIndex + 1) % state.bufferSize
		}
	}
}

// applyLowpassEffect は、ローパスフィルターエフェクトを適用します
func (p *AudioEffectProcessor) applyLowpassEffect(guildID string, pcmData []int16) {
	// フィルター状態を取得または作成
	p.lowpassStatesMutex.Lock()
	state, exists := p.lowpassStates[guildID]
	if !exists {
		state = &LowpassFilterState{
			lastOutputLeft:  0,
			lastOutputRight: 0,
		}
		p.lowpassStates[guildID] = state
	}
	p.lowpassStatesMutex.Unlock()

	// より強力なローパスフィルターのパラメータ
	// α値を非常に小さくして電話機のような効果を作る
	const alpha = float32(0.08) // カットオフ周波数をかなり低く（約800Hz）
	const oneMinusAlpha = 1.0 - alpha
	const volumeBoost = float32(1.4) // 音量を40%アップ（フィルター後の音量低下を補正）
	const muddyness = float32(0.15)  // 「こもり」効果を加える

	// ステレオ（2チャンネル）のPCMデータに対してフィルターを適用
	for i := 0; i < len(pcmData); i += 2 {
		if i+1 < len(pcmData) {
			// 左チャンネル
			inputLeft := float32(pcmData[i])

			// 1段目のローパスフィルター
			filteredLeft := alpha*inputLeft + oneMinusAlpha*state.lastOutputLeft

			// 2段目のローパス（より強力にするため）
			filteredLeft = alpha*filteredLeft + oneMinusAlpha*state.lastOutputLeft
			state.lastOutputLeft = filteredLeft

			// 「こもり」効果を追加（低音域を少し強調）
			muddyLeft := filteredLeft + muddyness*state.lastOutputLeft

			// 音量補正とクリッピング防止
			outputLeft := muddyLeft * volumeBoost
			if outputLeft > 32767 {
				outputLeft = 32767
			} else if outputLeft < -32768 {
				outputLeft = -32768
			}
			pcmData[i] = int16(outputLeft)

			// 右チャンネル
			inputRight := float32(pcmData[i+1])

			// 1段目のローパスフィルター
			filteredRight := alpha*inputRight + oneMinusAlpha*state.lastOutputRight

			// 2段目のローパス（より強力にするため）
			filteredRight = alpha*filteredRight + oneMinusAlpha*state.lastOutputRight
			state.lastOutputRight = filteredRight

			// 「こもり」効果を追加
			muddyRight := filteredRight + muddyness*state.lastOutputRight

			// 音量補正とクリッピング防止
			outputRight := muddyRight * volumeBoost
			if outputRight > 32767 {
				outputRight = 32767
			} else if outputRight < -32768 {
				outputRight = -32768
			}
			pcmData[i+1] = int16(outputRight)
		}
	}
}

// applyEchoEffect は、エコーエフェクトを適用します
func (p *AudioEffectProcessor) applyEchoEffect(guildID string, pcmData []int16) {
	// エコー状態を取得または作成
	p.echoStatesMutex.Lock()
	state, exists := p.echoStates[guildID]
	if !exists {
		// エコーのパラメータ
		delayMs := 150      // 150ms遅延
		sampleRate := 48000 // 48kHz
		delaySamples := (delayMs * sampleRate) / 1000

		state = &EchoFilterState{
			delayBufferLeft:  make([]float32, delaySamples),
			delayBufferRight: make([]float32, delaySamples),
			bufferIndex:      0,
			bufferSize:       delaySamples,
		}
		p.echoStates[guildID] = state
	}
	p.echoStatesMutex.Unlock()

	// エコーのパラメータ
	const feedback = float32(0.4) // エコーの強度（40%）
	const wetLevel = float32(0.3) // エコー音の音量（30%）
	const dryLevel = float32(0.7) // 原音の音量（70%）

	// ステレオ（2チャンネル）のPCMデータに対してエコーを適用
	for i := 0; i < len(pcmData); i += 2 {
		if i+1 < len(pcmData) {
			// 現在のサンプルを取得
			inputLeft := float32(pcmData[i])
			inputRight := float32(pcmData[i+1])

			// 遅延バッファから過去の音を取得
			delayedLeft := state.delayBufferLeft[state.bufferIndex]
			delayedRight := state.delayBufferRight[state.bufferIndex]

			// エコーを加えた新しい値を計算（フィードバック付き）
			newDelayLeft := inputLeft + delayedLeft*feedback
			newDelayRight := inputRight + delayedRight*feedback

			// 遅延バッファを更新
			state.delayBufferLeft[state.bufferIndex] = newDelayLeft
			state.delayBufferRight[state.bufferIndex] = newDelayRight

			// 出力 = 原音（ドライ）+ エコー音（ウェット）
			outputLeft := dryLevel*inputLeft + wetLevel*delayedLeft
			outputRight := dryLevel*inputRight + wetLevel*delayedRight

			// クリッピング防止
			if outputLeft > 32767 {
				outputLeft = 32767
			} else if outputLeft < -32768 {
				outputLeft = -32768
			}
			if outputRight > 32767 {
				outputRight = 32767
			} else if outputRight < -32768 {
				outputRight = -32768
			}

			pcmData[i] = int16(outputLeft)
			pcmData[i+1] = int16(outputRight)

			// バッファインデックスを進める（リングバッファ）
			state.bufferIndex = (state.bufferIndex + 1) % state.bufferSize
		}
	}
}
