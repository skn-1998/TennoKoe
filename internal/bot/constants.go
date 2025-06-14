package bot

// Audio processing constants
const (
	OpusSampleRate  = 48000                                                  // 48kHz
	OpusChannels    = 2                                                      // ステレオ
	OpusFrameSizeMs = 20                                                     // 20ms
	PCMFrameSize    = OpusSampleRate * OpusChannels * OpusFrameSizeMs / 1000 // 1920 samples
)

// AudioEffect は、音声エフェクトの種類を表します
type AudioEffect int

// 音声エフェクトの定数
const (
	EffectNone AudioEffect = iota
	EffectBitcrush
	EffectLowpass
	EffectEcho
	EffectHelium
)
