package bot

import (
	"log"
	"sync"
	"time"

	"layeh.com/gopus"
)

// AudioMixer は音声ミキシング処理を管理します
type AudioMixer struct {
	// PCMバッファ（ギルドごと）
	pcmBuffers      map[string]map[uint32][]int16 // guildID -> ssrc -> PCM buffer
	pcmBuffersMutex sync.Mutex

	// SpeakingBotへの出力チャネル（ギルドごと）
	speakingChannels      map[string]chan []byte // guildID -> Opus data channel
	speakingChannelsMutex sync.Mutex

	// ミキサーループ制御
	stopMixer chan bool
	running   bool
	runMutex  sync.Mutex

	// Opusエンコーダー（ギルドごと）
	encoders      map[string]*gopus.Encoder
	encodersMutex sync.Mutex

	// エフェクトプロセッサー
	effectProcessor *AudioEffectProcessor

	// SpeakingBotへの参照（エフェクト設定取得用）
	speakingBot *SpeakingBot

	// ログ制限用カウンター
	logCounter map[string]int
	logMutex   sync.Mutex
}

// NewAudioMixer は新しいAudioMixerを作成します
func NewAudioMixer(speakingBot *SpeakingBot) *AudioMixer {
	return &AudioMixer{
		pcmBuffers:       make(map[string]map[uint32][]int16),
		speakingChannels: make(map[string]chan []byte),
		encoders:         make(map[string]*gopus.Encoder),
		stopMixer:        make(chan bool),
		running:          false,
		effectProcessor:  NewAudioEffectProcessor(),
		speakingBot:      speakingBot,
		logCounter:       make(map[string]int),
	}
}

// SetSpeakingBot は、SpeakingBotの参照を設定します
func (m *AudioMixer) SetSpeakingBot(speakingBot *SpeakingBot) {
	m.speakingBot = speakingBot
}

// Start はミキサーループを開始します
func (m *AudioMixer) Start() {
	m.runMutex.Lock()
	defer m.runMutex.Unlock()

	if m.running {
		return
	}

	m.running = true
	go m.mixerLoop()
	log.Println("[AudioMixer] Mixer loop started")
}

// Stop はミキサーループを停止します
func (m *AudioMixer) Stop() {
	m.runMutex.Lock()
	defer m.runMutex.Unlock()

	if !m.running {
		return
	}

	m.running = false
	m.stopMixer <- true
	log.Println("[AudioMixer] Mixer loop stopped")
}

// RegisterSpeakingChannel は指定されたギルドIDにSpeakingBotのチャネルを登録します
func (m *AudioMixer) RegisterSpeakingChannel(guildID string, ch chan []byte) {
	m.speakingChannelsMutex.Lock()
	defer m.speakingChannelsMutex.Unlock()

	m.speakingChannels[guildID] = ch
	log.Printf("[AudioMixer:%s] Speaking channel registered", guildID)
}

// UnregisterSpeakingChannel は指定されたギルドIDのSpeakingBotチャネルを解除し閉じます
func (m *AudioMixer) UnregisterSpeakingChannel(guildID string) {
	m.speakingChannelsMutex.Lock()
	defer m.speakingChannelsMutex.Unlock()

	if ch, exists := m.speakingChannels[guildID]; exists {
		close(ch)
		delete(m.speakingChannels, guildID)
		log.Printf("[AudioMixer:%s] Speaking channel unregistered and closed", guildID)
	}
}

// HasSpeakingChannel は指定されたギルドIDにSpeakingBotチャネルが登録されているかチェックします
func (m *AudioMixer) HasSpeakingChannel(guildID string) bool {
	m.speakingChannelsMutex.Lock()
	defer m.speakingChannelsMutex.Unlock()

	_, exists := m.speakingChannels[guildID]
	return exists
}

// AddPCMData は指定されたギルドとSSRCにPCMデータを追加します
func (m *AudioMixer) AddPCMData(guildID string, ssrc uint32, pcmData []int16) {
	m.pcmBuffersMutex.Lock()
	defer m.pcmBuffersMutex.Unlock()

	if m.pcmBuffers[guildID] == nil {
		m.pcmBuffers[guildID] = make(map[uint32][]int16)
	}

	m.pcmBuffers[guildID][ssrc] = append(m.pcmBuffers[guildID][ssrc], pcmData...)
}

// CleanupGuildResources は指定されたギルドのリソースをクリーンアップします
func (m *AudioMixer) CleanupGuildResources(guildID string) {
	// PCMバッファをクリーンアップ
	m.pcmBuffersMutex.Lock()
	delete(m.pcmBuffers, guildID)
	m.pcmBuffersMutex.Unlock()

	// エンコーダーをクリーンアップ
	m.encodersMutex.Lock()
	delete(m.encoders, guildID)
	m.encodersMutex.Unlock()

	log.Printf("[AudioMixer:%s] Guild resources cleaned up", guildID)
}

// CleanupUserResources は指定されたSSRCのリソースをクリーンアップします
func (m *AudioMixer) CleanupUserResources(guildID string, ssrcs ...uint32) {
	m.pcmBuffersMutex.Lock()
	defer m.pcmBuffersMutex.Unlock()

	if guildBuffers, exists := m.pcmBuffers[guildID]; exists {
		for _, ssrc := range ssrcs {
			delete(guildBuffers, ssrc)
		}
	}
}

// CleanupAllResources はすべてのリソースをクリーンアップします
func (m *AudioMixer) CleanupAllResources() {
	m.pcmBuffersMutex.Lock()
	m.pcmBuffers = make(map[string]map[uint32][]int16)
	m.pcmBuffersMutex.Unlock()

	m.encodersMutex.Lock()
	m.encoders = make(map[string]*gopus.Encoder)
	m.encodersMutex.Unlock()

	log.Println("[AudioMixer] All resources cleaned up")
}

// mixerLoop はミキサーのメインループです
func (m *AudioMixer) mixerLoop() {
	ticker := time.NewTicker(time.Duration(MixerTickRate) * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-m.stopMixer:
			log.Println("[AudioMixer] Mixer loop stopping...")
			return
		case <-ticker.C:
			m.mixAndEncodeAllGuilds()
		}
	}
}

// mixAndEncodeAllGuilds はすべてのギルドの音声をミキシングしてエンコードします
func (m *AudioMixer) mixAndEncodeAllGuilds() {
	m.speakingChannelsMutex.Lock()
	guildIDs := make([]string, 0, len(m.speakingChannels))
	for guildID := range m.speakingChannels {
		guildIDs = append(guildIDs, guildID)
	}
	m.speakingChannelsMutex.Unlock()

	for _, guildID := range guildIDs {
		m.mixAndEncodeSingleGuild(guildID)
	}
}

// mixAndEncodeSingleGuild は単一ギルドの音声をミキシングしてエンコードします
func (m *AudioMixer) mixAndEncodeSingleGuild(guildID string) {
	// PCMバッファからデータを取得
	m.pcmBuffersMutex.Lock()
	guildBuffers, exists := m.pcmBuffers[guildID]
	if !exists || len(guildBuffers) == 0 {
		m.pcmBuffersMutex.Unlock()
		return
	}

	// 各SSRCから必要なサンプル数を取得してミキシング
	mixedPCM := make([]int16, PCMFrameSize*PCMChannels)
	hasData := false
	activeSSRCs := 0

	for ssrc, buffer := range guildBuffers {
		if len(buffer) >= PCMFrameSize*PCMChannels {
			hasData = true
			activeSSRCs++
			// 必要なサンプル数を取得
			samples := buffer[:PCMFrameSize*PCMChannels]
			// バッファから削除
			guildBuffers[ssrc] = buffer[PCMFrameSize*PCMChannels:]

			// ミキシング（加算）
			for i, sample := range samples {
				mixed := int32(mixedPCM[i]) + int32(sample)
				// クリッピング防止
				if mixed > 32767 {
					mixed = 32767
				} else if mixed < -32768 {
					mixed = -32768
				}
				mixedPCM[i] = int16(mixed)
			}
		}
	}
	m.pcmBuffersMutex.Unlock()

	if !hasData {
		return
	}

	// 音声データが処理されていることをログに記録（頻度を制限）
	if activeSSRCs > 0 {
		m.logMutex.Lock()
		m.logCounter[guildID]++
		if m.logCounter[guildID]%100 == 1 { // 100回に1回ログ出力
			log.Printf("[AudioMixer:%s] Processing audio data from %d active sources (count: %d)", guildID, activeSSRCs, m.logCounter[guildID])
		}
		m.logMutex.Unlock()
	}

	// エフェクトを適用
	if m.speakingBot != nil {
		effect := m.speakingBot.GetEffect(guildID)
		if effect != EffectNone {
			log.Printf("[AudioMixer:%s] Applying effect: %d", guildID, effect)
		}
		mixedPCM = m.effectProcessor.ApplyEffect(guildID, effect, mixedPCM)
	}

	// Opusエンコード
	opusData, err := m.encodePCMToOpus(guildID, mixedPCM)
	if err != nil {
		log.Printf("[AudioMixer:%s] Error encoding PCM to Opus: %v", guildID, err)
		return
	}

	// SpeakingBotに送信
	m.speakingChannelsMutex.Lock()
	if ch, exists := m.speakingChannels[guildID]; exists {
		select {
		case ch <- opusData:
			// 送信成功
		default:
			// チャネルがフル、スキップ
			log.Printf("[AudioMixer:%s] Speaking channel full, skipping audio data", guildID)
		}
	}
	m.speakingChannelsMutex.Unlock()
}

// encodePCMToOpus はPCMデータをOpusにエンコードします
func (m *AudioMixer) encodePCMToOpus(guildID string, pcmData []int16) ([]byte, error) {
	m.encodersMutex.Lock()
	encoder, exists := m.encoders[guildID]
	if !exists {
		var err error
		encoder, err = gopus.NewEncoder(OpusSampleRate, OpusChannels, gopus.Audio)
		if err != nil {
			m.encodersMutex.Unlock()
			return nil, err
		}
		m.encoders[guildID] = encoder
	}
	m.encodersMutex.Unlock()

	return encoder.Encode(pcmData, PCMFrameSize, BufferSize)
}
