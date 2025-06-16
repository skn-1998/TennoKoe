package bot

// 音声処理に関する定数
const (
	// Opus設定
	OpusSampleRate = 48000 // 48kHz
	OpusChannels   = 2     // ステレオ

	// PCM設定
	PCMFrameSize = 960 // 20ms at 48kHz
	PCMChannels  = 2   // ステレオ

	// バッファ設定
	BufferSize = 4096

	// ミキサー設定
	MixerTickRate = 20 // 20ms間隔
)

// AudioEffect は音声エフェクトの種類を表します
type AudioEffect int

const (
	EffectNone AudioEffect = iota
	EffectBitcrush
	EffectLowpass
	EffectEcho
	EffectHelium
)
