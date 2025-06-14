package bot

import (
	"log"
	"sync"
	"time"

	"layeh.com/gopus"
)

// AudioMixer は音声ミキシング処理を管理します
type AudioMixer struct {
	// PCMバッファ
	pcmBuffer   map[uint32][]int16 // SSRC -> PCM data buffer
	bufferMutex sync.Mutex

	// アクティブなギルドとSSRCの管理
	activeGuildSSRC  map[string]map[uint32]bool // guildID -> SSRC set
	activeGuildMutex sync.Mutex

	// Speaking チャンネル管理
	speakingChannels      map[string]chan []byte // guildID -> SpeakingBotへのOpusデータチャネル
	speakingChannelsMutex sync.Mutex

	// エフェクトプロセッサ
	effectProcessor *AudioEffectProcessor

	// ミキサー制御
	stopMixer chan struct{}
}

// NewAudioMixer は新しいAudioMixerを作成します
func NewAudioMixer() *AudioMixer {
	return &AudioMixer{
		pcmBuffer:        make(map[uint32][]int16),
		activeGuildSSRC:  make(map[string]map[uint32]bool),
		speakingChannels: make(map[string]chan []byte),
		effectProcessor:  NewAudioEffectProcessor(),
		stopMixer:        make(chan struct{}),
	}
}

// Start はミキサーを開始します
func (m *AudioMixer) Start() {
	go m.startMixer()
}

// Stop はミキサーを停止します
func (m *AudioMixer) Stop() {
	log.Println("[AudioMixer] Stopping mixer...")
	close(m.stopMixer)
}

// AddPCMData はPCMデータをバッファに追加します
func (m *AudioMixer) AddPCMData(ssrc uint32, pcmData []int16) {
	if len(pcmData) == 0 {
		return
	}

	m.bufferMutex.Lock()
	m.pcmBuffer[ssrc] = append(m.pcmBuffer[ssrc], pcmData...)
	m.bufferMutex.Unlock()
}

// TrackSSRC はギルドでアクティブなSSRCを記録します
func (m *AudioMixer) TrackSSRC(guildID string, ssrc uint32) {
	m.activeGuildMutex.Lock()
	defer m.activeGuildMutex.Unlock()
	if _, ok := m.activeGuildSSRC[guildID]; !ok {
		m.activeGuildSSRC[guildID] = make(map[uint32]bool)
	}
	m.activeGuildSSRC[guildID][ssrc] = true
}

// RegisterSpeakingChannel は、指定されたギルドIDにSpeakingBotのチャネルを登録します
func (m *AudioMixer) RegisterSpeakingChannel(guildID string, ch chan []byte) {
	m.speakingChannelsMutex.Lock()
	defer m.speakingChannelsMutex.Unlock()
	// 古いチャネルがあれば閉じる (念のため)
	if oldCh, exists := m.speakingChannels[guildID]; exists {
		close(oldCh)
	}
	m.speakingChannels[guildID] = ch
	log.Printf("[AudioMixer:%s] Registered speaking channel", guildID)
}

// UnregisterSpeakingChannel は、指定されたギルドIDのSpeakingBotチャネルを解除し閉じます
func (m *AudioMixer) UnregisterSpeakingChannel(guildID string) {
	m.speakingChannelsMutex.Lock()
	defer m.speakingChannelsMutex.Unlock()
	if ch, exists := m.speakingChannels[guildID]; exists {
		delete(m.speakingChannels, guildID)

		// チャンネルを安全に閉じる
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("[AudioMixer:%s] Speaking channel was already closed during unregistration", guildID)
				}
			}()

			if ch != nil {
				close(ch)
				log.Printf("[AudioMixer:%s] Unregistered and closed speaking channel", guildID)
			}
		}()
	} else {
		log.Printf("[AudioMixer:%s] Attempted to unregister non-existent speaking channel", guildID)
	}
}

// SetAudioEffect は音声エフェクトを設定します
func (m *AudioMixer) SetAudioEffect(guildID string, effect AudioEffect) {
	m.effectProcessor.SetAudioEffect(guildID, effect)
}

// HasSpeakingChannel は指定されたギルドにSpeakingChannelが存在するかチェックします
func (m *AudioMixer) HasSpeakingChannel(guildID string) bool {
	m.speakingChannelsMutex.Lock()
	defer m.speakingChannelsMutex.Unlock()
	_, exists := m.speakingChannels[guildID]
	return exists
}

// CleanupGuildResources は、特定のギルドに関連するすべてのリソースをクリーンアップします
func (m *AudioMixer) CleanupGuildResources(guildID string) {
	log.Printf("[AudioMixer:%s] Cleaning up all resources for guild", guildID)

	// アクティブなSSRCを取得してクリーンアップ
	m.activeGuildMutex.Lock()
	var ssrcsToClean []uint32
	if activeSSRCs, ok := m.activeGuildSSRC[guildID]; ok {
		for ssrc := range activeSSRCs {
			ssrcsToClean = append(ssrcsToClean, ssrc)
		}
		delete(m.activeGuildSSRC, guildID)
	}
	m.activeGuildMutex.Unlock()

	if len(ssrcsToClean) > 0 {
		m.cleanupUserResources(ssrcsToClean...)
	}

	// Speakingチャンネルもクリーンアップ
	m.UnregisterSpeakingChannel(guildID)

	// エフェクト状態もクリーンアップ
	m.effectProcessor.CleanupGuildEffects(guildID)
}

// CleanupUserResources は、指定されたSSRCリストに関連するPCMバッファをクリーンアップします
func (m *AudioMixer) CleanupUserResources(ssrcs ...uint32) {
	m.cleanupUserResources(ssrcs...)
}

// cleanupUserResources は、指定されたSSRCリストに関連するPCMバッファをクリーンアップします
func (m *AudioMixer) cleanupUserResources(ssrcs ...uint32) {
	m.bufferMutex.Lock()
	defer m.bufferMutex.Unlock()

	cleanedCount := 0
	for _, ssrc := range ssrcs {
		if _, ok := m.pcmBuffer[ssrc]; ok {
			delete(m.pcmBuffer, ssrc)
			cleanedCount++
		}
	}
	if cleanedCount > 0 {
		log.Printf("[AudioMixer] Cleaned up buffer resources for %d SSRC(s): %v", cleanedCount, ssrcs)
	}
}

// CleanupAllResources は、すべてのリソースをクリーンアップします
func (m *AudioMixer) CleanupAllResources() {
	log.Println("[AudioMixer] Cleaning up all resources...")

	m.bufferMutex.Lock()
	m.activeGuildMutex.Lock()
	m.speakingChannelsMutex.Lock()
	defer m.bufferMutex.Unlock()
	defer m.activeGuildMutex.Unlock()
	defer m.speakingChannelsMutex.Unlock()

	m.pcmBuffer = make(map[uint32][]int16)
	m.activeGuildSSRC = make(map[string]map[uint32]bool)

	// Speakingチャンネルも全て閉じてクリア
	for guildID, ch := range m.speakingChannels {
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("[AudioMixer] Speaking channel for guild %s was already closed during cleanup", guildID)
				}
			}()

			if ch != nil {
				close(ch)
				log.Printf("[AudioMixer] Closed speaking channel for guild %s during cleanup", guildID)
			}
		}()
	}
	m.speakingChannels = make(map[string]chan []byte)

	// エフェクト状態もクリア
	m.effectProcessor.CleanupAllEffects()

	log.Println("[AudioMixer] All resources cleaned up.")
}

// startMixer は、PCMバッファからデータを定期的に読み取り、ミキシングしてエンコードし、適切なSpeakingBotチャネルに送るループを開始します
func (m *AudioMixer) startMixer() {
	mixTicker := time.NewTicker(time.Duration(OpusFrameSizeMs) * time.Millisecond)
	defer mixTicker.Stop()
	log.Println("[AudioMixer] Starting mixer loop.")

	encoder, err := gopus.NewEncoder(OpusSampleRate, OpusChannels, gopus.Voip)
	if err != nil {
		log.Fatalf("[AudioMixer] Failed to create Opus encoder: %v", err)
		return
	}

	opusBuffer := make([]byte, 1024) // ループ外で確保して使い回す

	for {
		select {
		case <-m.stopMixer:
			log.Println("[AudioMixer] Received stop signal. Exiting mixer loop.")
			return
		case <-mixTicker.C:
			// ギルドごとにミキシングとエンコードを実行
			m.mixAndEncodeGuilds(encoder, opusBuffer)
		}
	}
}

// mixAndEncodeGuilds は、アクティブなギルドごとにミキシングとエンコードを実行します
func (m *AudioMixer) mixAndEncodeGuilds(encoder *gopus.Encoder, opusBuffer []byte) {
	// 処理対象のギルドIDリストを取得
	m.activeGuildMutex.Lock()
	guildsToProcess := make([]string, 0, len(m.activeGuildSSRC))
	for guildID := range m.activeGuildSSRC {
		guildsToProcess = append(guildsToProcess, guildID)
	}
	m.activeGuildMutex.Unlock()

	// 各ギルドについて処理
	for _, guildID := range guildsToProcess {
		m.mixAndEncodeSingleGuild(guildID, encoder, opusBuffer)
	}
}

// mixAndEncodeSingleGuild は、指定されたギルドのPCMバッファをミキシングし、エンコードして対応するSpeakingチャネルに送信します
func (m *AudioMixer) mixAndEncodeSingleGuild(guildID string, encoder *gopus.Encoder, opusBuffer []byte) {
	// このギルドに対応するSpeaking Channelがあるか確認
	m.speakingChannelsMutex.Lock()
	speakingCh, exists := m.speakingChannels[guildID]
	m.speakingChannelsMutex.Unlock()
	if !exists {
		return // 送信先がない
	}

	// チャンネルがnilでないことも確認
	if speakingCh == nil {
		log.Printf("[AudioMixer:%s] Speaking channel is nil, removing from map", guildID)
		m.speakingChannelsMutex.Lock()
		delete(m.speakingChannels, guildID)
		m.speakingChannelsMutex.Unlock()
		return
	}

	mixedPCM := make([]int16, PCMFrameSize) // 毎回初期化
	activeStreams := 0

	// このギルドに属するSSRCを取得
	m.activeGuildMutex.Lock()
	ssrcsInGuild := make([]uint32, 0)
	if ssrcMap, ok := m.activeGuildSSRC[guildID]; ok {
		for ssrc := range ssrcMap {
			ssrcsInGuild = append(ssrcsInGuild, ssrc)
		}
	} else {
		m.activeGuildMutex.Unlock()
		return
	}
	m.activeGuildMutex.Unlock()

	if len(ssrcsInGuild) == 0 {
		return // このギルドにアクティブなSSRCがない
	}

	m.bufferMutex.Lock()
	// このギルドのSSRCのバッファから読み出してミキシング
	for _, ssrc := range ssrcsInGuild {
		if buffer, ok := m.pcmBuffer[ssrc]; ok && len(buffer) >= PCMFrameSize {
			activeStreams++
			for i := 0; i < PCMFrameSize; i++ {
				sample := int32(mixedPCM[i]) + int32(buffer[i])
				if sample > 32767 {
					sample = 32767
				} else if sample < -32768 {
					sample = -32768
				}
				mixedPCM[i] = int16(sample)
			}
			m.pcmBuffer[ssrc] = buffer[PCMFrameSize:] // 読み出した分を削除
		}
	}
	m.bufferMutex.Unlock()

	if activeStreams > 0 {
		// 音声エフェクトを適用
		m.effectProcessor.ApplyAudioEffect(guildID, mixedPCM)

		encodedOpus, err := encoder.Encode(mixedPCM, PCMFrameSize/OpusChannels, len(opusBuffer))
		if err != nil {
			log.Printf("[AudioMixer:%s] Failed to encode mixed PCM: %v", guildID, err)
			return
		}

		if len(encodedOpus) > 0 {
			// 対応するSpeaking Channelに送信 (非ブロッキング)
			// チャンネルが閉じられている場合のパニックを防止
			func() {
				defer func() {
					if r := recover(); r != nil {
						log.Printf("[AudioMixer:%s] Speaking channel was closed, discarding Opus data (%d bytes)", guildID, len(encodedOpus))
					}
				}()

				select {
				case speakingCh <- encodedOpus:
					// log.Printf("[AudioMixer:%s] Sent %d bytes of Opus data", guildID, len(encodedOpus))
				default:
					log.Printf("[AudioMixer:%s] Speaking channel is blocked, discarding Opus data (%d bytes)", guildID, len(encodedOpus))
				}
			}()
		}
	}
}
