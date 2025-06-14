package bot

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	// "tennokoe-bot/internal/audio" // 不要に

	"github.com/bwmarrin/discordgo"
)

// SpeakingBot は、音声を再生するためのBotです
type SpeakingBot struct {
	session *discordgo.Session
	// audioProcessor *audio.AudioProcessor // 削除
	listeningBot  *ListeningBot                         // ★ ListeningBotへの参照を追加
	voiceConn     map[string]*discordgo.VoiceConnection // guildID -> VoiceConnection
	audioChannels map[string]chan []byte                // ★ ギルドごとのOpusデータチャネル (guildID -> channel)
	channelsMutex sync.Mutex                            // ★ audioChannelsアクセス用のmutex
	stopChan      chan struct{}
}

// NewSpeakingBot は、新しいSpeakingBotインスタンスを作成します
func NewSpeakingBot(token string, listeningBot *ListeningBot) (*SpeakingBot, error) { // 引数を変更
	// Discordセッションを作成
	session, err := discordgo.New("Bot " + token)
	if err != nil {
		return nil, fmt.Errorf("failed to create Discord session: %w", err)
	}

	// インテントを設定
	session.Identify.Intents = discordgo.IntentsGuildMessages |
		discordgo.IntentsGuildVoiceStates |
		discordgo.IntentsGuilds |
		discordgo.IntentsMessageContent

	// ★ ギルドごとのaudioChannelマップを作成
	bot := &SpeakingBot{
		session: session,
		// audioProcessor: audioProcessor, // 削除
		listeningBot:  listeningBot, // ★ ListeningBotをセット
		voiceConn:     make(map[string]*discordgo.VoiceConnection),
		audioChannels: make(map[string]chan []byte), // ★ ギルドごとのチャネルマップ
		stopChan:      make(chan struct{}),
	}

	// メッセージハンドラを登録
	session.AddHandler(bot.messageHandler)
	session.AddHandler(bot.interactionHandler)

	// ★ audioProcessor.AddListener は削除 (handleSpeakCommand で Register する)
	// audioProcessor.AddListener(audioChannel)

	return bot, nil
}

// Start は、Botを起動します
func (b *SpeakingBot) Start() error {
	err := b.session.Open()
	if err != nil {
		return fmt.Errorf("failed to open Discord session: %w", err)
	}

	log.Printf("スピーキングBot (%s) が起動しました", b.session.State.User.Username)
	return nil
}

// Stop は、Botを停止します
func (b *SpeakingBot) Stop() {
	log.Println("[SpeakingBot] Stopping...")
	// 停止信号を送信
	close(b.stopChan)

	// すべてのボイス接続を切断し、ListeningBotから登録解除
	for guildID, conn := range b.voiceConn {
		log.Printf("[SpeakingBot:%s] Unregistering speaking channel from ListeningBot", guildID)
		// ★ ListeningBotから登録解除
		b.listeningBot.UnregisterSpeakingChannel(guildID)

		log.Printf("[SpeakingBot:%s] Disconnecting voice connection", guildID)
		conn.Disconnect()
		delete(b.voiceConn, guildID)
	}

	// すべてのオーディオチャネルを安全に閉じる
	b.channelsMutex.Lock()
	for guildID, ch := range b.audioChannels {
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("[SpeakingBot:%s] Audio channel was already closed during stop", guildID)
				}
			}()

			if ch != nil {
				close(ch)
				log.Printf("[SpeakingBot:%s] Closed audio channel", guildID)
			}
		}()
	}
	b.audioChannels = make(map[string]chan []byte)
	b.channelsMutex.Unlock()

	// ★ audioProcessor.RemoveListener は削除
	// b.audioProcessor.RemoveListener(b.audioChannel)

	// セッションを閉じる
	b.session.Close()
	log.Println("[SpeakingBot] Stopped.")
}

// messageHandler は、メッセージを処理します
func (b *SpeakingBot) messageHandler(s *discordgo.Session, m *discordgo.MessageCreate) {
	// 自分自身のメッセージは無視
	if m.Author.ID == s.State.User.ID || m.Author.Bot {
		return
	}

	// heaven!speakコマンドを処理
	if strings.ToLower(m.Content) == "t!speak" {
		log.Printf("[SpeakingBot] Received command: %s from %s", m.Content, m.Author.Username)
		b.handleSpeakCommand(s, m)
	} else if strings.ToLower(m.Content) == "t!disconnect" {
		log.Printf("[SpeakingBot] Received command: %s from %s", m.Content, m.Author.Username)
		b.handleDisconnectCommand(s, m)
	}
}

// handleSpeakCommand は、speakコマンドを処理します
func (b *SpeakingBot) handleSpeakCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
	guildID := m.GuildID

	// 既に接続している場合は切断
	if conn, exists := b.voiceConn[guildID]; exists {
		log.Printf("[SpeakingBot:%s] Already connected, disconnecting first", guildID)
		// ListeningBotから登録解除
		b.listeningBot.UnregisterSpeakingChannel(guildID)
		conn.Disconnect()
		delete(b.voiceConn, guildID)

		s.ChannelMessageSend(m.ChannelID, "🔇 **SpeakingBot切断完了**\n音声の再生を停止しました。")
		return
	}

	// 音声エフェクト選択メニューを表示
	b.showAudioEffectSelector(s, m)
}

// showAudioEffectSelector は、音声エフェクト選択用のセレクトメニューを表示します
func (b *SpeakingBot) showAudioEffectSelector(s *discordgo.Session, m *discordgo.MessageCreate) {
	// 音声エフェクト選択メニューを作成
	selectMenu := discordgo.SelectMenu{
		CustomID: "audio_effect_select_speak", // t!speakコマンド用
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
		Content: "🎤 **音声エフェクトを選択してください**\n" +
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
		log.Printf("[SpeakingBot] Error sending effect selector: %v", err)
		s.ChannelMessageSend(m.ChannelID, "❌ **エラー**\n"+
			"エフェクト選択メニューの表示に失敗しました。")
		return
	}
}

// interactionHandler は、インタラクション（セレクトメニューなど）を処理します
func (b *SpeakingBot) interactionHandler(s *discordgo.Session, i *discordgo.InteractionCreate) {
	// セレクトメニューの場合
	if i.Type == discordgo.InteractionMessageComponent {
		data := i.MessageComponentData()

		// 音声エフェクト選択の場合（t!speak用）
		if data.CustomID == "audio_effect_select_speak" {
			b.handleEffectSelection(s, i)
		}
	}
}

// handleEffectSelection は、エフェクト選択の処理を行います
func (b *SpeakingBot) handleEffectSelection(s *discordgo.Session, i *discordgo.InteractionCreate) {
	data := i.MessageComponentData()
	guildID := i.GuildID

	if len(data.Values) == 0 {
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseUpdateMessage,
			Data: &discordgo.InteractionResponseData{
				Content: "❌ **エラー**\n" +
					"エフェクトが選択されませんでした。\n" +
					"再度 `t!speak` コマンドを実行してください。",
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

	// ListeningBotにエフェクト設定を通知
	b.listeningBot.audioMixer.SetAudioEffect(guildID, selectedEffect)

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
		success := b.connectToVoiceChannel(s, guildID, i.Member.User.ID)

		// 接続結果を元のメッセージに反映
		var finalContent string
		if success {
			finalContent = fmt.Sprintf("✅ **天の声ボット接続完了**\n"+
				"選択されたエフェクト: **%s**\n"+
				"音声の再生を開始しました！", effectName)
		} else {
			finalContent = fmt.Sprintf("❌ **接続エラー**\n"+
				"選択されたエフェクト: **%s**\n"+
				"ボイスチャンネルへの接続に失敗しました。", effectName)
		}

		// メッセージを最終的な状態に更新
		_, err := s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
			Content: &finalContent,
		})
		if err != nil {
			log.Printf("[SpeakingBot] Error updating interaction message: %v", err)
		}
	}()
}

// ConnectToVoiceChannel は、ボイスチャンネルへの接続処理を行います（公開メソッド）
func (b *SpeakingBot) ConnectToVoiceChannel(s *discordgo.Session, guildID, userID string) bool {
	return b.connectToVoiceChannel(s, guildID, userID)
}

// connectToVoiceChannel は、ボイスチャンネルへの接続処理を行います
func (b *SpeakingBot) connectToVoiceChannel(s *discordgo.Session, guildID, userID string) bool {
	// ユーザーがボイスチャンネルにいるか確認
	guild, err := s.State.Guild(guildID)
	if err != nil {
		log.Printf("[SpeakingBot] Error getting guild: %v", err)
		return false
	}

	var userVoiceState *discordgo.VoiceState
	for _, vs := range guild.VoiceStates {
		if vs.UserID == userID {
			userVoiceState = vs
			break
		}
	}

	if userVoiceState == nil {
		log.Printf("[SpeakingBot] User %s is not in a voice channel", userID)
		return false
	}

	// 既存の接続を切断し、登録解除
	if conn, exists := b.voiceConn[guildID]; exists {
		log.Printf("[SpeakingBot:%s] Disconnecting from existing voice channel", guildID)
		// ListeningBotから登録解除
		b.listeningBot.UnregisterSpeakingChannel(guildID)
		conn.Disconnect()
		delete(b.voiceConn, guildID)
	}

	// ボイスチャンネルに接続
	log.Printf("[SpeakingBot:%s] Joining voice channel %s", guildID, userVoiceState.ChannelID)
	conn, err := s.ChannelVoiceJoin(guildID, userVoiceState.ChannelID, true, false) // Mute=true, Deaf=false
	if err != nil {
		log.Printf("[SpeakingBot:%s] Error joining voice channel: %v", guildID, err)
		return false
	}

	// 接続を保存
	b.voiceConn[guildID] = conn

	// このギルド用の新しい音声チャネルを作成
	b.channelsMutex.Lock()
	// 既存のチャネルがあれば安全に閉じる
	if oldCh, exists := b.audioChannels[guildID]; exists {
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("[SpeakingBot:%s] Audio channel was already closed during reconnection", guildID)
				}
			}()

			if oldCh != nil {
				close(oldCh)
				log.Printf("[SpeakingBot:%s] Closed old audio channel during reconnection", guildID)
			}
		}()
	}
	audioChannel := make(chan []byte, 100)
	b.audioChannels[guildID] = audioChannel
	b.channelsMutex.Unlock()

	// ListeningBotにこのギルド用の音声受信チャネルを登録
	b.listeningBot.RegisterSpeakingChannel(guildID, audioChannel)

	// 音声再生ループを開始
	go b.playAudioLoop(conn, audioChannel)

	// 接続成功メッセージ
	channel, err := s.Channel(userVoiceState.ChannelID)
	if err != nil {
		log.Printf("[SpeakingBot:%s] Error getting channel info: %v", guildID, err)
	} else {
		log.Printf("[SpeakingBot] Successfully connected to voice channel %s in guild %s", channel.Name, guildID)
	}

	return true
}

// ConnectToVoiceChannelByID は、指定されたチャンネルIDに接続します（公開メソッド）
func (b *SpeakingBot) ConnectToVoiceChannelByID(s *discordgo.Session, guildID, channelID string) bool {
	return b.connectToVoiceChannelByID(s, guildID, channelID)
}

// connectToVoiceChannelByID は、指定されたチャンネルIDに接続します
func (b *SpeakingBot) connectToVoiceChannelByID(s *discordgo.Session, guildID, channelID string) bool {
	// 既存の接続を切断し、登録解除
	if conn, exists := b.voiceConn[guildID]; exists {
		log.Printf("[SpeakingBot:%s] Disconnecting from existing voice channel", guildID)
		// ListeningBotから登録解除
		b.listeningBot.UnregisterSpeakingChannel(guildID)
		conn.Disconnect()
		delete(b.voiceConn, guildID)
	}

	// ボイスチャンネルに接続
	log.Printf("[SpeakingBot:%s] Joining voice channel %s", guildID, channelID)
	conn, err := s.ChannelVoiceJoin(guildID, channelID, true, false) // Mute=true, Deaf=false
	if err != nil {
		log.Printf("[SpeakingBot:%s] Error joining voice channel: %v", guildID, err)
		return false
	}

	// 接続が完了するまで少し待機
	time.Sleep(500 * time.Millisecond)

	// 接続を保存
	b.voiceConn[guildID] = conn

	// このギルド用の新しい音声チャネルを作成
	b.channelsMutex.Lock()
	// 既存のチャネルがあれば安全に閉じる
	if oldCh, exists := b.audioChannels[guildID]; exists {
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("[SpeakingBot:%s] Audio channel was already closed during reconnection", guildID)
				}
			}()

			if oldCh != nil {
				close(oldCh)
				log.Printf("[SpeakingBot:%s] Closed old audio channel during reconnection", guildID)
			}
		}()
	}
	audioChannel := make(chan []byte, 100)
	b.audioChannels[guildID] = audioChannel
	b.channelsMutex.Unlock()

	// ListeningBotにこのギルド用の音声受信チャネルを登録
	b.listeningBot.RegisterSpeakingChannel(guildID, audioChannel)

	// 音声再生ループを開始
	go b.playAudioLoop(conn, audioChannel)

	// 接続成功メッセージ
	channel, err := s.Channel(channelID)
	if err != nil {
		log.Printf("[SpeakingBot:%s] Error getting channel info: %v", guildID, err)
	} else {
		log.Printf("[SpeakingBot] Successfully connected to voice channel %s in guild %s", channel.Name, guildID)
	}

	return true
}

// playAudioLoop は、音声を再生するループを実行します
func (b *SpeakingBot) playAudioLoop(vc *discordgo.VoiceConnection, audioChannel chan []byte) {
	// 音声接続が準備できるまで待機
	log.Printf("[SpeakingBot:%s] playAudioLoop started, waiting for connection...", vc.GuildID)
	time.Sleep(1 * time.Second)
	log.Printf("[SpeakingBot:%s] Connection ready, starting audio playback loop", vc.GuildID)

	defer func() {
		// 終了時にチャネルをクリーンアップ
		guildID := vc.GuildID
		b.channelsMutex.Lock()
		if ch, exists := b.audioChannels[guildID]; exists && ch == audioChannel {
			delete(b.audioChannels, guildID)

			// チャンネルを安全に閉じる
			func() {
				defer func() {
					if r := recover(); r != nil {
						log.Printf("[SpeakingBot:%s] Audio channel was already closed during cleanup", guildID)
					}
				}()

				if ch != nil {
					close(ch)
					log.Printf("[SpeakingBot:%s] Cleaned up audio channel", guildID)
				}
			}()
		}
		b.channelsMutex.Unlock()
	}()

	// 音声を再生するループ
	for {
		select {
		case <-b.stopChan:
			// 停止信号を受信
			log.Printf("[SpeakingBot:%s] Stop signal received, exiting playAudioLoop", vc.GuildID)
			return
		case audioData, ok := <-audioChannel:
			if !ok {
				// チャンネルが閉じられた
				log.Printf("[SpeakingBot:%s] Audio channel closed, exiting playAudioLoop", vc.GuildID)
				return
			}

			// log.Printf("[SpeakingBot:%s] Received audio data from channel (Size: %d)", vc.GuildID, len(audioData)) // コメントアウト

			// 音声データがあれば再生
			if len(audioData) > 0 {
				// 音声データをOpusフレームに変換して送信
				// discordgoでは、Opusフレームをそのまま送信できる
				// log.Printf("[SpeakingBot:%s] Sending Opus data to voice channel (Size: %d)", vc.GuildID, len(audioData)) // コメントアウト
				vc.OpusSend <- audioData
			}
		}
	}
}

// handleDisconnectCommand は、disconnectコマンドを処理します
func (b *SpeakingBot) handleDisconnectCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
	guildID := m.GuildID
	disconnected := false

	// SpeakingBot の接続を切断
	if conn, exists := b.voiceConn[guildID]; exists {
		log.Printf("[SpeakingBot] Disconnecting from voice channel in guild %s", guildID)

		// ListeningBotから登録解除
		b.listeningBot.UnregisterSpeakingChannel(guildID)

		// 音声接続を切断
		conn.Disconnect()
		delete(b.voiceConn, guildID)

		// 音声チャンネルをクリーンアップ
		b.channelsMutex.Lock()
		if ch, exists := b.audioChannels[guildID]; exists {
			func() {
				defer func() {
					if r := recover(); r != nil {
						log.Printf("[SpeakingBot:%s] Audio channel was already closed during disconnect", guildID)
					}
				}()

				if ch != nil {
					close(ch)
					log.Printf("[SpeakingBot:%s] Closed audio channel during disconnect", guildID)
				}
			}()
			delete(b.audioChannels, guildID)
		}
		b.channelsMutex.Unlock()

		disconnected = true
	}

	if disconnected {
		log.Printf("[SpeakingBot] Successfully disconnected from guild %s", guildID)
	} else {
		log.Printf("[SpeakingBot] No connection to disconnect in guild %s", guildID)
	}
}
