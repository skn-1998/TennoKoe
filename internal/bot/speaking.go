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
	guildEffects  map[string]AudioEffect                // ★ ギルドごとのエフェクト設定 (guildID -> effect)
	effectsMutex  sync.Mutex                            // ★ guildEffectsアクセス用のmutex
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
		guildEffects:  make(map[string]AudioEffect), // ★ ギルドごとのエフェクトマップ
		stopChan:      make(chan struct{}),
	}

	// メッセージハンドラはCommandRouterで管理されるため、ここでは登録しない
	// インタラクションハンドラーのみ登録
	session.AddHandler(bot.interactionHandler)

	// ★ audioProcessor.AddListener は削除 (handleSpeakCommand で Register する)
	// audioProcessor.AddListener(audioChannel)

	return bot, nil
}

// GetSession は、Discordセッションを取得します（公開メソッド）
func (b *SpeakingBot) GetSession() *discordgo.Session {
	return b.session
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

// HasConnection は、指定されたギルドIDに接続があるかチェックします
func (b *SpeakingBot) HasConnection(guildID string) bool {
	_, exists := b.voiceConn[guildID]
	return exists
}

// SetEffect は、指定されたギルドIDにエフェクトを設定します
func (b *SpeakingBot) SetEffect(guildID string, effect AudioEffect) {
	b.effectsMutex.Lock()
	defer b.effectsMutex.Unlock()
	b.guildEffects[guildID] = effect
	log.Printf("[SpeakingBot:%s] Effect set to: %d", guildID, effect)
}

// GetEffect は、指定されたギルドIDのエフェクトを取得します
func (b *SpeakingBot) GetEffect(guildID string) AudioEffect {
	b.effectsMutex.Lock()
	defer b.effectsMutex.Unlock()
	if effect, exists := b.guildEffects[guildID]; exists {
		return effect
	}
	return EffectNone // デフォルトは通常
}

// SetEffectByName は、エフェクト名でエフェクトを設定します
func (b *SpeakingBot) SetEffectByName(guildID, effectName string) {
	var effect AudioEffect
	switch effectName {
	case "ビットクラッシュ":
		effect = EffectBitcrush
	case "ローパスフィルター":
		effect = EffectLowpass
	case "エコー":
		effect = EffectEcho
	case "ヘリウム声":
		effect = EffectHelium
	default:
		effect = EffectNone
	}
	b.SetEffect(guildID, effect)
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
	log.Printf("[SpeakingBot] Received interaction: Type=%d", i.Type)

	// セレクトメニューの場合
	if i.Type == discordgo.InteractionMessageComponent {
		data := i.MessageComponentData()
		log.Printf("[SpeakingBot] Processing interaction with CustomID: %s", data.CustomID)

		// 音声エフェクト選択の場合（t!speak用）
		if data.CustomID == "audio_effect_select_speak" {
			log.Printf("[SpeakingBot] Handling audio effect selection")
			b.handleEffectSelection(s, i)
			return
		}

		// SpeakingBotのチャンネル選択を処理（t!speak用）
		if strings.HasPrefix(data.CustomID, "channel_select_speak_") {
			log.Printf("[SpeakingBot] Handling t!speak channel selection")
			b.handleChannelSelectionForSpeak(s, i)
			return
		}

		log.Printf("[SpeakingBot] No matching handler for CustomID: %s", data.CustomID)
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
	var effectName string

	switch selectedValue {
	case "effect_none":
		effectName = "通常"
	case "effect_bitcrush":
		effectName = "ビットクラッシュ"
	case "effect_lowpass":
		effectName = "ローパスフィルター"
	case "effect_echo":
		effectName = "エコー"
	case "effect_helium":
		effectName = "ヘリウム声"
	default:
		effectName = "通常"
	}

	// ListeningBotにエフェクト設定を通知
	// エフェクトを設定（現在の実装では個別設定は不要）
	log.Printf("[SpeakingBot:%s] Selected effect: %s", guildID, effectName)

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

	// チャンネル選択メニューを表示
	go func() {
		// 少し待ってからフォローアップメッセージでチャンネル選択メニューを表示
		time.Sleep(500 * time.Millisecond)
		b.showChannelSelectorForSpeak(s, i, effectName)
	}()
}

// showChannelSelectorForSpeak は、SpeakingBot用のボイスチャンネル選択メニューを表示します
func (b *SpeakingBot) showChannelSelectorForSpeak(s *discordgo.Session, i *discordgo.InteractionCreate, effectName string) {
	guildID := i.GuildID

	// ギルド情報を取得
	guild, err := s.State.Guild(guildID)
	if err != nil {
		log.Printf("[SpeakingBot:%s] Error getting guild: %v", guildID, err)
		s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
			Content: "❌ **エラー**\nサーバー情報の取得に失敗しました。",
		})
		return
	}

	// ボイスチャンネルを取得
	var voiceChannels []*discordgo.Channel
	for _, channel := range guild.Channels {
		if channel.Type == discordgo.ChannelTypeGuildVoice {
			voiceChannels = append(voiceChannels, channel)
		}
	}

	if len(voiceChannels) == 0 {
		s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
			Content: "❌ **エラー**\nこのサーバーにはボイスチャンネルがありません。",
		})
		return
	}

	// セレクトメニューのオプションを作成
	var options []discordgo.SelectMenuOption
	for _, channel := range voiceChannels {
		// チャンネル内のユーザー数を取得
		userCount := 0
		for _, vs := range guild.VoiceStates {
			if vs.ChannelID == channel.ID {
				userCount++
			}
		}

		description := fmt.Sprintf("👥 %d人", userCount)
		if userCount == 0 {
			description = "空のチャンネル"
		}

		options = append(options, discordgo.SelectMenuOption{
			Label:       channel.Name,
			Value:       fmt.Sprintf("speak_%s", channel.ID),
			Description: description,
			Emoji: &discordgo.ComponentEmoji{
				Name: "🔊",
			},
		})
	}

	// セレクトメニューコンポーネントを作成
	selectMenu := discordgo.SelectMenu{
		CustomID:    fmt.Sprintf("channel_select_speak_%s_%s_%s", guildID, i.Member.User.ID, effectName),
		Placeholder: "接続するボイスチャンネルを選択してください",
		Options:     options,
		MinValues:   &[]int{1}[0],
		MaxValues:   1,
	}

	// フォローアップメッセージを送信
	_, err = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
		Content: fmt.Sprintf("🔊 **SpeakingBot - チャンネル選択**\n選択されたエフェクト: **%s**\n音声を再生するボイスチャンネルを選択してください：", effectName),
		Components: []discordgo.MessageComponent{
			discordgo.ActionsRow{
				Components: []discordgo.MessageComponent{selectMenu},
			},
		},
	})

	if err != nil {
		log.Printf("[SpeakingBot:%s] Error sending channel selector: %v", guildID, err)
		s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
			Content: "❌ **エラー**\nチャンネル選択メニューの表示に失敗しました。",
		})
	}
}

// handleChannelSelectionForSpeak は、SpeakingBot用のチャンネル選択を処理します
func (b *SpeakingBot) handleChannelSelectionForSpeak(s *discordgo.Session, i *discordgo.InteractionCreate) {
	log.Printf("[SpeakingBot] handleChannelSelectionForSpeak: Starting channel selection processing")

	data := i.MessageComponentData()
	guildID := i.GuildID

	log.Printf("[SpeakingBot:%s] Processing channel selection with %d values", guildID, len(data.Values))

	// 選択されたチャンネルIDを取得
	if len(data.Values) == 0 {
		log.Printf("[SpeakingBot:%s] No values selected", guildID)
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "❌ **エラー**\nチャンネルが選択されていません。",
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
		return
	}

	selectedValue := data.Values[0]
	log.Printf("[SpeakingBot:%s] Selected value: %s", guildID, selectedValue)

	if len(selectedValue) < 6 || selectedValue[:6] != "speak_" {
		log.Printf("[SpeakingBot:%s] Invalid selection format: %s", guildID, selectedValue)
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "❌ **エラー**\n無効な選択です。",
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
		return
	}

	channelID := selectedValue[6:] // "speak_"を除去
	log.Printf("[SpeakingBot:%s] Extracted channelID: %s", guildID, channelID)

	// CustomIDからエフェクト名を抽出
	customIDParts := strings.Split(data.CustomID, "_")
	effectName := "通常"
	if len(customIDParts) >= 5 {
		effectName = customIDParts[4] // channel_select_speak_guildID_userID_effectName
	}

	// インタラクションに応答
	log.Printf("[SpeakingBot:%s] Responding to interaction", guildID)
	err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: fmt.Sprintf("🔊 **SpeakingBot接続中...**\n選択されたエフェクト: **%s**\n選択されたチャンネルに接続しています...", effectName),
		},
	})
	if err != nil {
		log.Printf("[SpeakingBot:%s] Error responding to interaction: %v", guildID, err)
		return
	}

	// ボイスチャンネルに接続
	log.Printf("[SpeakingBot:%s] Attempting to connect to voice channel: %s", guildID, channelID)
	success := b.ConnectToVoiceChannelByID(s, guildID, channelID)

	// 結果をフォローアップメッセージで送信
	var followupContent string
	if success {
		followupContent = fmt.Sprintf("✅ **SpeakingBot接続完了**\n選択されたエフェクト: **%s**\n音声の再生を開始しました！", effectName)
		log.Printf("[SpeakingBot:%s] Successfully connected to voice channel %s", guildID, channelID)
	} else {
		followupContent = fmt.Sprintf("❌ **接続エラー**\n選択されたエフェクト: **%s**\nボイスチャンネルへの接続に失敗しました。", effectName)
		log.Printf("[SpeakingBot:%s] Failed to connect to voice channel %s", guildID, channelID)
	}

	log.Printf("[SpeakingBot:%s] Sending followup message", guildID)
	_, err = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
		Content: followupContent,
	})
	if err != nil {
		log.Printf("[SpeakingBot:%s] Error sending followup message: %v", guildID, err)
	}

	log.Printf("[SpeakingBot:%s] handleChannelSelectionForSpeak completed", guildID)
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
	conn, err := s.ChannelVoiceJoin(guildID, userVoiceState.ChannelID, false, true) // Mute=false, Deaf=true
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
	conn, err := s.ChannelVoiceJoin(guildID, channelID, false, true) // Mute=false, Deaf=true
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
				// Speaking状態を開始
				vc.Speaking(true)

				// 音声データをOpusフレームに変換して送信
				// discordgoでは、Opusフレームをそのまま送信できる
				// log.Printf("[SpeakingBot:%s] Sending Opus data to voice channel (Size: %d)", vc.GuildID, len(audioData)) // コメントアウト
				vc.OpusSend <- audioData

				// 短い遅延の後、Speaking状態を停止
				time.Sleep(20 * time.Millisecond)
				vc.Speaking(false)
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
