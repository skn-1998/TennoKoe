package bot

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"layeh.com/gopus"
)

// ListeningBot は、音声を受信しミキシングするためのBotです
type ListeningBot struct {
	session       *discordgo.Session
	voiceConn     map[string]*discordgo.VoiceConnection // guildID -> VoiceConnection
	decoders      map[uint32]*gopus.Decoder             // SSRC -> Opus Decoder
	decodersMutex sync.Mutex

	// 音声ミキサー
	audioMixer *AudioMixer

	// SpeakingBotへの参照
	speakingBot *SpeakingBot
}

// NewListeningBot は、新しいListeningBotインスタンスを作成します
func NewListeningBot(token string) (*ListeningBot, error) {
	session, err := discordgo.New("Bot " + token)
	if err != nil {
		return nil, fmt.Errorf("failed to create Discord session: %w", err)
	}
	session.Identify.Intents = discordgo.IntentsGuildMessages |
		discordgo.IntentsGuildVoiceStates |
		discordgo.IntentsGuilds |
		discordgo.IntentsMessageContent

	bot := &ListeningBot{
		session:     session,
		voiceConn:   make(map[string]*discordgo.VoiceConnection),
		decoders:    make(map[uint32]*gopus.Decoder),
		audioMixer:  NewAudioMixer(),
		speakingBot: nil, // 後でSetSpeakingBotで設定
	}

	session.AddHandler(bot.messageHandler)
	session.AddHandler(bot.interactionHandler)

	// ミキサー処理を開始
	bot.audioMixer.Start()

	return bot, nil
}

// SetSpeakingBot は、SpeakingBotの参照を設定します
func (b *ListeningBot) SetSpeakingBot(speakingBot *SpeakingBot) {
	b.speakingBot = speakingBot
}

// Start は、Botを起動します
func (b *ListeningBot) Start() error {
	err := b.session.Open()
	if err != nil {
		return fmt.Errorf("failed to open Discord session: %w", err)
	}
	log.Printf("リスニングBot (%s) が起動しました", b.session.State.User.Username)
	return nil
}

// Stop は、Botを停止します
func (b *ListeningBot) Stop() {
	log.Println("[ListeningBot] Stopping...")
	// ミキサーループを停止
	log.Println("[ListeningBot] Stopping mixer loop...")
	b.audioMixer.Stop()

	// すべてのボイス接続を切断
	for guildID, conn := range b.voiceConn {
		log.Printf("[ListeningBot:%s] Disconnecting voice connection", guildID)
		conn.Disconnect()
		delete(b.voiceConn, guildID)
	}

	// クリーンアップ (SSRCリソースとSpeakingチャンネル)
	b.cleanupAllResources()

	// セッションを閉じる
	b.session.Close()
	log.Println("[ListeningBot] Stopped.")
}

// RegisterSpeakingChannel は、指定されたギルドIDにSpeakingBotのチャネルを登録します
func (b *ListeningBot) RegisterSpeakingChannel(guildID string, ch chan []byte) {
	b.audioMixer.RegisterSpeakingChannel(guildID, ch)
}

// UnregisterSpeakingChannel は、指定されたギルドIDのSpeakingBotチャネルを解除し閉じます
func (b *ListeningBot) UnregisterSpeakingChannel(guildID string) {
	b.audioMixer.UnregisterSpeakingChannel(guildID)
}

// messageHandler は、メッセージを処理します
func (b *ListeningBot) messageHandler(s *discordgo.Session, m *discordgo.MessageCreate) {
	// 自分自身のメッセージは無視
	if m.Author.ID == s.State.User.ID || m.Author.Bot {
		return
	}

	// コマンドを処理
	switch strings.ToLower(m.Content) {
	case "t!listen":
		log.Printf("[ListeningBot] Received command: %s from %s", m.Content, m.Author.Username)
		b.handleListenCommand(s, m)
	case "t!disconnect":
		log.Printf("[ListeningBot] Received command: %s from %s", m.Content, m.Author.Username)
		b.handleDisconnectCommand(s, m)
	}
}

// handleListenCommand は、listenコマンドを処理します
func (b *ListeningBot) handleListenCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
	guildID := m.GuildID

	// 既に接続している場合は切断
	if conn, exists := b.voiceConn[guildID]; exists {
		log.Printf("[ListeningBot:%s] Already connected, disconnecting first", guildID)
		conn.Disconnect()
		delete(b.voiceConn, guildID)
		b.cleanupGuildResources(guildID)

		s.ChannelMessageSend(m.ChannelID, "🔇 **ListeningBot切断完了**\n音声の受信を停止しました。")
		return
	}

	// 音声エフェクト選択メニューを表示
	b.showAudioEffectSelector(s, m)
}

// showAudioEffectSelector は、音声エフェクト選択用のセレクトメニューを表示します
func (b *ListeningBot) showAudioEffectSelector(s *discordgo.Session, m *discordgo.MessageCreate) {
	// 音声エフェクト選択メニューを作成
	selectMenu := discordgo.SelectMenu{
		CustomID: "audio_effect_select", // t!listenコマンド用
		Options: []discordgo.SelectMenuOption{
			{
				Label: "通常",
				Value: "effect_none",
				Emoji: &discordgo.ComponentEmoji{
					Name: "🔊",
				},
			},
			{
				Label: "ビットクラッシュ",
				Value: "effect_bitcrush",
				Emoji: &discordgo.ComponentEmoji{
					Name: "🎮",
				},
			},
			{
				Label: "ローパスフィルター",
				Value: "effect_lowpass",
				Emoji: &discordgo.ComponentEmoji{
					Name: "📻",
				},
			},
			{
				Label: "エコー",
				Value: "effect_echo",
				Emoji: &discordgo.ComponentEmoji{
					Name: "🏔️",
				},
			},
			{
				Label: "ヘリウム声",
				Value: "effect_helium",
				Emoji: &discordgo.ComponentEmoji{
					Name: "🎈",
				},
			},
		},
		Placeholder: "音声エフェクトを選択してください",
	}

	// メッセージを送信
	_, err := s.ChannelMessageSendComplex(m.ChannelID, &discordgo.MessageSend{
		Content: "🎧 **音声エフェクトを選択してください**\n" +
			"以下のメニューから希望のエフェクトを選択してください。",
		Components: []discordgo.MessageComponent{
			discordgo.ActionsRow{
				Components: []discordgo.MessageComponent{
					selectMenu,
				},
			},
		},
	})
	if err != nil {
		log.Printf("[ListeningBot] Error sending effect selector: %v", err)
		s.ChannelMessageSend(m.ChannelID, "❌ **エラー**\n"+
			"エフェクト選択メニューの表示に失敗しました。")
		return
	}
}

// interactionHandler は、インタラクション（セレクトメニューなど）を処理します
func (b *ListeningBot) interactionHandler(s *discordgo.Session, i *discordgo.InteractionCreate) {
	// セレクトメニューの場合
	if i.Type == discordgo.InteractionMessageComponent {
		data := i.MessageComponentData()

		// 音声エフェクト選択の場合（t!listen用）
		if data.CustomID == "audio_effect_select" {
			b.handleEffectSelection(s, i)
		}
	}
}

// handleEffectSelection は、エフェクト選択の処理を行います
func (b *ListeningBot) handleEffectSelection(s *discordgo.Session, i *discordgo.InteractionCreate) {
	data := i.MessageComponentData()
	guildID := i.GuildID

	if len(data.Values) == 0 {
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseUpdateMessage,
			Data: &discordgo.InteractionResponseData{
				Content: "❌ **エラー**\n" +
					"エフェクトが選択されませんでした。\n" +
					"再度 `t!listen` コマンドを実行してください。",
				Components: []discordgo.MessageComponent{}, // セレクトメニューを削除
			},
		})
		return
	}

	selectedValue := data.Values[0]
	var selectedEffect AudioEffect
	var effectName string

	switch selectedValue {
	case "effect_none":
		selectedEffect = EffectNone
		effectName = "通常"
	case "effect_bitcrush":
		selectedEffect = EffectBitcrush
		effectName = "ビットクラッシュ"
	case "effect_lowpass":
		selectedEffect = EffectLowpass
		effectName = "ローパスフィルター"
	case "effect_echo":
		selectedEffect = EffectEcho
		effectName = "エコー"
	case "effect_helium":
		selectedEffect = EffectHelium
		effectName = "ヘリウム声"
	default:
		selectedEffect = EffectNone
		effectName = "通常"
	}

	// エフェクトを設定
	b.audioMixer.SetAudioEffect(guildID, selectedEffect)

	// セレクトメニューを削除してエフェクト選択結果メッセージに置き換え
	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseUpdateMessage,
		Data: &discordgo.InteractionResponseData{
			Content: fmt.Sprintf("✅ **エフェクト選択完了**\n"+
				"選択されたエフェクト: **%s**\n\n"+
				"ボイスチャンネルに接続しています...", effectName),
			Components: []discordgo.MessageComponent{}, // コンポーネントを削除（セレクトメニューを閉じる）
		},
	})

	// ボイスチャンネルに接続
	go func() {
		b.connectToVoiceChannel(s, guildID, i.Member.User.ID)

		// 接続結果を元のメッセージに反映
		finalContent := fmt.Sprintf("✅ **天の声ボット接続完了**\n"+
			"選択されたエフェクト: **%s**\n"+
			"音声の受信を開始しました！", effectName)

		// メッセージを最終的な状態に更新
		_, err := s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
			Content: &finalContent,
		})
		if err != nil {
			log.Printf("[ListeningBot] Error updating interaction message: %v", err)
		}
	}()
}

// handleDisconnectCommand は、disconnectコマンドを処理します
func (b *ListeningBot) handleDisconnectCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
	// このギルド内での天の声ボット接続を確認
	guildID := m.GuildID
	listeningDisconnected := false
	speakingDisconnected := false

	// ListeningBot の接続を切断
	if conn, exists := b.voiceConn[guildID]; exists {
		log.Printf("[ListeningBot] Disconnecting from voice channel in guild %s", guildID)
		conn.Disconnect()
		delete(b.voiceConn, guildID)

		// このギルドのリソースをクリーンアップ
		b.cleanupGuildResources(guildID)
		listeningDisconnected = true
	}

	// SpeakingBotの接続状況を確認 (SpeakingChannelの登録状況で判断)
	if b.audioMixer.HasSpeakingChannel(guildID) {
		// SpeakingBotが接続されている場合は、SpeakingChannelを閉じることで
		// SpeakingBotの音声再生ループを停止させる
		log.Printf("[ListeningBot] Forcing speaking bot disconnection for guild %s", guildID)
		b.UnregisterSpeakingChannel(guildID)
		speakingDisconnected = true
	}

	// 結果に応じたメッセージを送信
	if listeningDisconnected || speakingDisconnected {
		message := "**天の声ボットを切断しました**\n"
		if listeningDisconnected {
			message += "・音声の受信を停止しました\n"
		}
		if speakingDisconnected {
			message += "・音声の再生を停止しました\n"
		}

		s.ChannelMessageSend(m.ChannelID, message)
		log.Printf("[ListeningBot] Successfully disconnected from guild %s (Listening: %v, Speaking: %v)",
			guildID, listeningDisconnected, speakingDisconnected)
	} else {
		s.ChannelMessageSend(m.ChannelID, "❓ **天の声ボットは接続されていません**\n"+
			"現在このサーバーでは音声の送受信を行っていません。")
		log.Printf("[ListeningBot] No connections to disconnect in guild %s", guildID)
	}
}

// connectToVoiceChannel は、ボイスチャンネルへの接続処理を行います
func (b *ListeningBot) connectToVoiceChannel(s *discordgo.Session, guildID, userID string) {
	// ユーザーがボイスチャンネルにいるか確認
	guild, err := s.State.Guild(guildID)
	if err != nil {
		log.Printf("[ListeningBot] Error getting guild: %v", err)
		return
	}

	var userVoiceState *discordgo.VoiceState
	for _, vs := range guild.VoiceStates {
		if vs.UserID == userID {
			userVoiceState = vs
			break
		}
	}

	if userVoiceState == nil {
		log.Printf("[ListeningBot] User %s is not in a voice channel", userID)
		return
	}

	// 既存の接続を切断
	if conn, exists := b.voiceConn[guildID]; exists {
		log.Printf("[ListeningBot] Disconnecting from existing voice channel in guild %s", guildID)
		conn.Disconnect()
		delete(b.voiceConn, guildID)
	}

	// ボイスチャンネルに接続
	log.Printf("[ListeningBot] Joining voice channel %s in guild %s", userVoiceState.ChannelID, guildID)
	conn, err := s.ChannelVoiceJoin(guildID, userVoiceState.ChannelID, false, false)
	if err != nil {
		log.Printf("[ListeningBot] Error joining voice channel: %v", err)
		return
	}

	// 接続を保存
	b.voiceConn[guildID] = conn

	// Opus受信ループを開始
	go b.opusReceiveLoop(conn)

	// 音声受信ハンドラを登録
	conn.AddHandler(b.voicePacketHandler)

	log.Printf("[ListeningBot] Successfully connected to voice channel in guild %s", guildID)
}

// voicePacketHandler は、発話状態の変更をログに出力します
func (b *ListeningBot) voicePacketHandler(vc *discordgo.VoiceConnection, vs *discordgo.VoiceSpeakingUpdate) {
	// このハンドラは Opus パケット自体は処理しない
	// パケットの処理は opusReceiveLoop で行われる
	if vs.Speaking {
		log.Printf("[ListeningBot:%s] Voice speaking state update: User %s Speaking=true (SSRC: %d)", vc.GuildID, vs.UserID, vs.SSRC)
	} else {
		log.Printf("[ListeningBot:%s] Voice speaking state update: User %s Speaking=false (SSRC: %d)", vc.GuildID, vs.UserID, vs.SSRC)
	}
}

// opusReceiveLoop は、指定されたボイスコネクションからOpusパケットを受信し続けます
func (b *ListeningBot) opusReceiveLoop(vc *discordgo.VoiceConnection) {
	guildID := vc.GuildID
	log.Printf("[ListeningBot:%s] Starting Opus receive loop", guildID)

	var lastPacketReceived = make(map[uint32]time.Time) // SSRCごとの最終受信時刻
	cleanupTicker := time.NewTicker(30 * time.Second)   // 30秒ごとにチェック
	defer cleanupTicker.Stop()

	for {
		select {
		case packet, ok := <-vc.OpusRecv:
			if !ok {
				log.Printf("[ListeningBot:%s] OpusRecv channel closed. Exiting loop.", guildID)
				b.cleanupGuildResources(guildID) // 関連リソースをクリーンアップ
				return
			}

			// SSRCを記録 (クリーンアップ用)
			b.audioMixer.TrackSSRC(guildID, packet.SSRC)
			lastPacketReceived[packet.SSRC] = time.Now()

			// ユーザーごとのデコーダーを取得または作成
			decoder, err := b.getOrCreateDecoder(packet.SSRC)
			if err != nil {
				log.Printf("[ListeningBot:%s] Error getting/creating decoder for SSRC %d: %v", guildID, packet.SSRC, err)
				continue
			}

			// OpusパケットをPCMにデコード
			decodedPCM, err := decoder.Decode(packet.Opus, PCMFrameSize/OpusChannels, false) // FECなし
			if err != nil {
				continue // エラー時はバッファに追加しない
			}

			// デコードされたPCMデータをバッファに追加
			if len(decodedPCM) > 0 { // デコード結果が空でないことを確認
				b.audioMixer.AddPCMData(packet.SSRC, decodedPCM)
			}

		case <-cleanupTicker.C:
			// 一定時間パケットを受信していないSSRCのリソースをクリーンアップ
			now := time.Now()
			var ssrcsToClean []uint32

			for ssrc, lastTime := range lastPacketReceived {
				if now.Sub(lastTime) > 60*time.Second { // 60秒以上受信なし
					ssrcsToClean = append(ssrcsToClean, ssrc)
				}
			}

			if len(ssrcsToClean) > 0 {
				log.Printf("[ListeningBot:%s] Cleaning up inactive SSRC resources: %v", guildID, ssrcsToClean)
				b.cleanupUserResources(ssrcsToClean...)
				// lastPacketReceivedからも削除
				for _, ssrc := range ssrcsToClean {
					delete(lastPacketReceived, ssrc)
				}
			}
		}
	}
}

// getOrCreateDecoder は、指定されたSSRCに対応するOpusデコーダーを返すか、なければ作成します
func (b *ListeningBot) getOrCreateDecoder(ssrc uint32) (*gopus.Decoder, error) {
	b.decodersMutex.Lock()
	defer b.decodersMutex.Unlock()

	if decoder, ok := b.decoders[ssrc]; ok {
		return decoder, nil
	}

	// 新しいデコーダーを作成
	decoder, err := gopus.NewDecoder(OpusSampleRate, OpusChannels)
	if err != nil {
		return nil, fmt.Errorf("failed to create Opus decoder for SSRC %d: %w", ssrc, err)
	}
	b.decoders[ssrc] = decoder
	log.Printf("[ListeningBot] Created new Opus decoder for SSRC %d", ssrc)
	return decoder, nil
}

// cleanupGuildResources は、特定のギルドに関連するすべてのリソースをクリーンアップします
func (b *ListeningBot) cleanupGuildResources(guildID string) {
	log.Printf("[ListeningBot:%s] Cleaning up all resources for guild", guildID)
	b.audioMixer.CleanupGuildResources(guildID)
}

// cleanupUserResources は、指定されたSSRCリストに関連するデコーダーをクリーンアップします
func (b *ListeningBot) cleanupUserResources(ssrcs ...uint32) {
	b.decodersMutex.Lock()
	defer b.decodersMutex.Unlock()

	cleanedCount := 0
	for _, ssrc := range ssrcs {
		if _, ok := b.decoders[ssrc]; ok {
			delete(b.decoders, ssrc)
			cleanedCount++
		}
	}
	if cleanedCount > 0 {
		log.Printf("[ListeningBot] Cleaned up decoder resources for %d SSRC(s): %v", cleanedCount, ssrcs)
	}

	// AudioMixerのリソースもクリーンアップ
	b.audioMixer.CleanupUserResources(ssrcs...)
}

// cleanupAllResources は、すべてのリソースをクリーンアップします
func (b *ListeningBot) cleanupAllResources() {
	log.Println("[ListeningBot] Cleaning up all resources...")

	b.decodersMutex.Lock()
	defer b.decodersMutex.Unlock()

	b.decoders = make(map[uint32]*gopus.Decoder)
	b.audioMixer.CleanupAllResources()

	log.Println("[ListeningBot] All resources cleaned up.")
}
