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
		audioMixer:  NewAudioMixer(nil), // SpeakingBotは後で設定
		speakingBot: nil,                // 後でSetSpeakingBotで設定
	}

	// インタラクションハンドラーを登録
	session.AddHandler(bot.interactionHandler)

	// ミキサー処理を開始
	bot.audioMixer.Start()

	return bot, nil
}

// SetSpeakingBot は、SpeakingBotの参照を設定します
func (b *ListeningBot) SetSpeakingBot(speakingBot *SpeakingBot) {
	b.speakingBot = speakingBot
	// AudioMixerにもSpeakingBotを設定
	b.audioMixer.SetSpeakingBot(speakingBot)
}

// GetSession は、Discordセッションを取得します（公開メソッド）
func (b *ListeningBot) GetSession() *discordgo.Session {
	return b.session
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

// HandleListenCommand は、listenコマンドを処理します（公開メソッド）
func (b *ListeningBot) HandleListenCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
	guildID := m.GuildID

	// SpeakingBotが接続している場合は切断
	if b.speakingBot != nil && b.speakingBot.HasConnection(guildID) {
		log.Printf("[ListeningBot:%s] SpeakingBot is connected, disconnecting first", guildID)
		b.speakingBot.handleDisconnectCommand(s, m)
		time.Sleep(1 * time.Second) // 切断完了を待つ
	}

	// 既に接続している場合は切断
	if conn, exists := b.voiceConn[guildID]; exists {
		log.Printf("[ListeningBot:%s] Already connected, disconnecting first", guildID)
		conn.Disconnect()
		delete(b.voiceConn, guildID)
		b.cleanupGuildResources(guildID)

		s.ChannelMessageSend(m.ChannelID, "🔇 **ListeningBot切断完了**\n音声の受信を停止しました。")
		return
	}

	// チャンネル選択メニューを表示
	b.showChannelSelector(s, m)
}

// showChannelSelector は、ボイスチャンネル選択メニューを表示します
func (b *ListeningBot) showChannelSelector(s *discordgo.Session, m *discordgo.MessageCreate) {
	guildID := m.GuildID

	// ギルド情報を取得
	guild, err := s.State.Guild(guildID)
	if err != nil {
		log.Printf("[ListeningBot:%s] Error getting guild: %v", guildID, err)
		s.ChannelMessageSend(m.ChannelID, "❌ **エラー**\nサーバー情報の取得に失敗しました。")
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
		s.ChannelMessageSend(m.ChannelID, "❌ **エラー**\nこのサーバーにはボイスチャンネルがありません。")
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
			Value:       fmt.Sprintf("listen_%s", channel.ID),
			Description: description,
			Emoji: &discordgo.ComponentEmoji{
				Name: "🎧",
			},
		})
	}

	// セレクトメニューコンポーネントを作成
	selectMenu := discordgo.SelectMenu{
		CustomID:    fmt.Sprintf("channel_select_listen_%s_%s", guildID, m.Author.ID),
		Placeholder: "接続するボイスチャンネルを選択してください",
		Options:     options,
		MinValues:   &[]int{1}[0],
		MaxValues:   1,
	}

	// メッセージを送信
	_, err = s.ChannelMessageSendComplex(m.ChannelID, &discordgo.MessageSend{
		Content: "🎧 **ListeningBot - チャンネル選択**\n音声を受信するボイスチャンネルを選択してください：",
		Components: []discordgo.MessageComponent{
			discordgo.ActionsRow{
				Components: []discordgo.MessageComponent{selectMenu},
			},
		},
	})

	if err != nil {
		log.Printf("[ListeningBot:%s] Error sending channel selector: %v", guildID, err)
		s.ChannelMessageSend(m.ChannelID, "❌ **エラー**\nチャンネル選択メニューの表示に失敗しました。")
	}
}

// interactionHandler は、インタラクション（セレクトメニューなど）を処理します
func (b *ListeningBot) interactionHandler(s *discordgo.Session, i *discordgo.InteractionCreate) {
	log.Printf("[ListeningBot] Received interaction: Type=%d", i.Type)

	if i.Type != discordgo.InteractionMessageComponent {
		log.Printf("[ListeningBot] Interaction is not a message component, ignoring")
		return
	}

	data := i.MessageComponentData()
	log.Printf("[ListeningBot] Processing interaction with CustomID: %s", data.CustomID)

	// ListeningBotのチャンネル選択を処理（t!listen用）
	if strings.HasPrefix(data.CustomID, "channel_select_listen_") {
		log.Printf("[ListeningBot] Handling t!listen channel selection")
		b.handleChannelSelection(s, i)
		return
	}

	// t!connect用のリスニングボットチャンネル選択を処理
	if strings.HasPrefix(data.CustomID, "channel_select_connect_listening_") {
		log.Printf("[ListeningBot] Handling t!connect listening channel selection")
		b.handleConnectListeningChannelSelection(s, i)
		return
	}

	// t!connect用のエフェクト選択を処理
	if strings.HasPrefix(data.CustomID, "audio_effect_select_connect_") {
		log.Printf("[ListeningBot] Handling t!connect effect selection")
		b.handleConnectEffectSelection(s, i)
		return
	}

	// t!connect用のスピーキングボットチャンネル選択を処理
	if strings.HasPrefix(data.CustomID, "channel_select_connect_speaking_") {
		log.Printf("[ListeningBot] Handling t!connect speaking channel selection")
		b.handleConnectSpeakingChannelSelection(s, i)
		return
	}

	// 旧形式のt!connect用のチャンネル選択を処理（後方互換性）
	if strings.HasPrefix(data.CustomID, "channel_select_connect_") {
		log.Printf("[ListeningBot] Handling legacy t!connect channel selection")
		b.handleConnectChannelSelection(s, i)
		return
	}

	log.Printf("[ListeningBot] No matching handler for CustomID: %s", data.CustomID)
}

// handleChannelSelection は、チャンネル選択を処理します
func (b *ListeningBot) handleChannelSelection(s *discordgo.Session, i *discordgo.InteractionCreate) {
	log.Printf("[ListeningBot] handleChannelSelection: Starting channel selection processing")

	data := i.MessageComponentData()
	guildID := i.GuildID

	log.Printf("[ListeningBot:%s] Processing channel selection with %d values", guildID, len(data.Values))

	// 選択されたチャンネルIDを取得
	if len(data.Values) == 0 {
		log.Printf("[ListeningBot:%s] No values selected", guildID)
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
	log.Printf("[ListeningBot:%s] Selected value: %s", guildID, selectedValue)

	if len(selectedValue) < 7 || selectedValue[:7] != "listen_" {
		log.Printf("[ListeningBot:%s] Invalid selection format: %s", guildID, selectedValue)
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "❌ **エラー**\n無効な選択です。",
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
		return
	}

	channelID := selectedValue[7:] // "listen_"を除去
	log.Printf("[ListeningBot:%s] Extracted channelID: %s", guildID, channelID)

	// インタラクションに応答
	log.Printf("[ListeningBot:%s] Responding to interaction", guildID)
	err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: "🎧 **ListeningBot接続中...**\n選択されたチャンネルに接続しています...",
		},
	})
	if err != nil {
		log.Printf("[ListeningBot:%s] Error responding to interaction: %v", guildID, err)
		return
	}

	// ボイスチャンネルに接続
	log.Printf("[ListeningBot:%s] Attempting to connect to voice channel: %s", guildID, channelID)
	success := b.ConnectToVoiceChannelByID(s, guildID, channelID)

	// 結果をフォローアップメッセージで送信
	var followupContent string
	if success {
		followupContent = "✅ **ListeningBot接続完了**\n音声の受信を開始しました！"
		log.Printf("[ListeningBot:%s] Successfully connected to voice channel %s", guildID, channelID)
	} else {
		followupContent = "❌ **接続エラー**\nボイスチャンネルへの接続に失敗しました。"
		log.Printf("[ListeningBot:%s] Failed to connect to voice channel %s", guildID, channelID)
	}

	log.Printf("[ListeningBot:%s] Sending followup message", guildID)
	_, err = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
		Content: followupContent,
	})
	if err != nil {
		log.Printf("[ListeningBot:%s] Error sending followup message: %v", guildID, err)
	}

	log.Printf("[ListeningBot:%s] handleChannelSelection completed", guildID)
}

// handleConnectChannelSelection は、t!connect用のチャンネル選択を処理します
func (b *ListeningBot) handleConnectChannelSelection(s *discordgo.Session, i *discordgo.InteractionCreate) {
	data := i.MessageComponentData()

	guildID := i.GuildID

	// 選択されたチャンネルIDを取得
	if len(data.Values) == 0 {
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
	if len(selectedValue) < 8 || selectedValue[:8] != "connect_" {
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "❌ **エラー**\n無効な選択です。",
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
		return
	}

	channelID := selectedValue[8:] // "connect_"を除去

	// インタラクションに応答
	err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: "🔗 **両ボット接続中...**\n選択されたチャンネルに両方のボットを接続しています...",
		},
	})
	if err != nil {
		log.Printf("[ListeningBot:%s] Error responding to interaction: %v", guildID, err)
		return
	}

	// ListeningBotを接続
	listeningSuccess := b.ConnectToVoiceChannelByID(s, guildID, channelID)

	// SpeakingBotを接続
	var speakingSuccess bool
	if b.speakingBot != nil {
		speakingSession := b.speakingBot.GetSession()
		speakingSuccess = b.speakingBot.ConnectToVoiceChannelByID(speakingSession, guildID, channelID)
	}

	// 結果をフォローアップメッセージで送信
	var followupContent string
	if listeningSuccess && speakingSuccess {
		followupContent = "✅ **両ボット接続完了**\nListeningBotとSpeakingBotの接続が完了しました！\n\n🎧 **ListeningBot**: 音声受信開始\n🔊 **SpeakingBot**: 音声再生準備完了"
		log.Printf("[ListeningBot:%s] Successfully connected both bots to voice channel %s", guildID, channelID)
	} else if listeningSuccess {
		followupContent = "⚠️ **部分的接続**\nListeningBotは接続しましたが、SpeakingBotの接続に失敗しました。"
		log.Printf("[ListeningBot:%s] ListeningBot connected but SpeakingBot failed for channel %s", guildID, channelID)
	} else if speakingSuccess {
		followupContent = "⚠️ **部分的接続**\nSpeakingBotは接続しましたが、ListeningBotの接続に失敗しました。"
		log.Printf("[ListeningBot:%s] SpeakingBot connected but ListeningBot failed for channel %s", guildID, channelID)
	} else {
		followupContent = "❌ **接続エラー**\n両方のボットの接続に失敗しました。"
		log.Printf("[ListeningBot:%s] Both bots failed to connect to channel %s", guildID, channelID)
	}

	_, err = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
		Content: followupContent,
	})
	if err != nil {
		log.Printf("[ListeningBot:%s] Error sending followup message: %v", guildID, err)
	}
}

// handleConnectListeningChannelSelection は、t!connect用のリスニングボットチャンネル選択を処理します
func (b *ListeningBot) handleConnectListeningChannelSelection(s *discordgo.Session, i *discordgo.InteractionCreate) {
	data := i.MessageComponentData()
	guildID := i.GuildID

	// 選択されたチャンネルIDを取得
	if len(data.Values) == 0 {
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
	if len(selectedValue) < 18 || selectedValue[:18] != "connect_listening_" {
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "❌ **エラー**\n無効な選択です。",
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
		return
	}

	listeningChannelID := selectedValue[18:] // "connect_listening_"を除去

	// インタラクションに応答
	err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: "🎧 **リスニングボット接続中...**\n選択されたチャンネルに接続しています...",
		},
	})
	if err != nil {
		log.Printf("[ListeningBot:%s] Error responding to interaction: %v", guildID, err)
		return
	}

	// ListeningBotを接続
	listeningSuccess := b.ConnectToVoiceChannelByID(s, guildID, listeningChannelID)

	if listeningSuccess {
		// 成功した場合、次のステップ（エフェクト選択）を表示
		log.Printf("[ListeningBot:%s] Successfully connected ListeningBot, showing effect selector", guildID)

		followupContent := "✅ **リスニングボット接続完了**\n🎧 音声の受信を開始しました！\n\n次に、スピーキングボット用のエフェクトを選択してください..."

		_, err = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
			Content: followupContent,
		})
		if err != nil {
			log.Printf("[ListeningBot:%s] Error sending followup message: %v", guildID, err)
		}

		// エフェクト選択メニューを表示
		b.showEffectSelectorForConnect(s, i, listeningChannelID)

	} else {
		followupContent := "❌ **接続エラー**\nリスニングボットのボイスチャンネルへの接続に失敗しました。"
		log.Printf("[ListeningBot:%s] Failed to connect ListeningBot to channel %s", guildID, listeningChannelID)

		_, err = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
			Content: followupContent,
		})
		if err != nil {
			log.Printf("[ListeningBot:%s] Error sending followup message: %v", guildID, err)
		}
	}
}

// handleConnectSpeakingChannelSelection は、t!connect用のスピーキングボットチャンネル選択を処理します
func (b *ListeningBot) handleConnectSpeakingChannelSelection(s *discordgo.Session, i *discordgo.InteractionCreate) {
	data := i.MessageComponentData()
	guildID := i.GuildID

	// 選択されたチャンネルIDを取得
	if len(data.Values) == 0 {
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
	if len(selectedValue) < 17 || selectedValue[:17] != "connect_speaking_" {
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "❌ **エラー**\n無効な選択です。",
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
		return
	}

	// "connect_speaking_listeningChannelID_speakingChannelID_effectName" の形式から抽出
	parts := selectedValue[17:] // "connect_speaking_"を除去
	channelParts := strings.Split(parts, "_")
	if len(channelParts) < 2 {
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "❌ **エラー**\n無効な選択形式です。",
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
		return
	}

	listeningChannelID := channelParts[0]
	speakingChannelID := channelParts[1]

	// エフェクト名を取得（3番目以降の部分、アンダースコアで結合されている可能性がある）
	var effectName string
	if len(channelParts) >= 3 {
		effectName = strings.Join(channelParts[2:], "_")
	} else {
		effectName = "通常"
	}

	// インタラクションに応答
	err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: "🔊 **スピーキングボット接続中...**\n選択されたチャンネルに接続しています...",
		},
	})
	if err != nil {
		log.Printf("[ListeningBot:%s] Error responding to interaction: %v", guildID, err)
		return
	}

	// SpeakingBotにエフェクトを設定してから接続
	var speakingSuccess bool
	if b.speakingBot != nil {
		// エフェクトを設定
		b.speakingBot.SetEffectByName(guildID, effectName)

		// 接続
		speakingSession := b.speakingBot.GetSession()
		speakingSuccess = b.speakingBot.ConnectToVoiceChannelByID(speakingSession, guildID, speakingChannelID)
	}

	// 結果をフォローアップメッセージで送信
	var followupContent string
	if speakingSuccess {
		followupContent = fmt.Sprintf("✅ **両ボット接続完了**\n🎧 **リスニングボット**: 音声受信開始\n🔊 **スピーキングボット**: 音声再生準備完了（エフェクト: %s）\n\n両方のボットが別々のチャンネルに接続されました！", effectName)
		log.Printf("[ListeningBot:%s] Successfully connected both bots - Listening: %s, Speaking: %s, Effect: %s", guildID, listeningChannelID, speakingChannelID, effectName)
	} else {
		followupContent = "⚠️ **部分的接続**\nリスニングボットは接続済みですが、スピーキングボットの接続に失敗しました。"
		log.Printf("[ListeningBot:%s] ListeningBot connected but SpeakingBot failed - Listening: %s, Speaking: %s, Effect: %s", guildID, listeningChannelID, speakingChannelID, effectName)
	}

	_, err = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
		Content: followupContent,
	})
	if err != nil {
		log.Printf("[ListeningBot:%s] Error sending followup message: %v", guildID, err)
	}
}

// showSpeakingChannelSelectorFromListening は、リスニングボットから呼び出されるスピーキングチャンネル選択を表示します
func (b *ListeningBot) showSpeakingChannelSelectorFromListening(s *discordgo.Session, m *discordgo.MessageCreate, listeningChannelID string) {
	guildID := m.GuildID

	// ギルド情報を取得
	guild, err := s.State.Guild(guildID)
	if err != nil {
		log.Printf("[ListeningBot:%s] Error getting guild: %v", guildID, err)
		s.ChannelMessageSend(m.ChannelID, "❌ **エラー**\nサーバー情報の取得に失敗しました。")
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
		s.ChannelMessageSend(m.ChannelID, "❌ **エラー**\nこのサーバーにはボイスチャンネルがありません。")
		return
	}

	// リスニングチャンネル名を取得
	var listeningChannelName string
	for _, channel := range voiceChannels {
		if channel.ID == listeningChannelID {
			listeningChannelName = channel.Name
			break
		}
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

		// リスニングチャンネルと同じ場合は表示を変更
		if channel.ID == listeningChannelID {
			description += " (リスニングと同じ)"
		}

		options = append(options, discordgo.SelectMenuOption{
			Label:       channel.Name,
			Value:       fmt.Sprintf("connect_speaking_%s_%s", listeningChannelID, channel.ID),
			Description: description,
			Emoji: &discordgo.ComponentEmoji{
				Name: "🔊",
			},
		})
	}

	// セレクトメニューコンポーネントを作成
	selectMenu := discordgo.SelectMenu{
		CustomID:    fmt.Sprintf("channel_select_connect_speaking_%s_%s_%s", guildID, m.Author.ID, listeningChannelID),
		Placeholder: "スピーキングボットを接続するチャンネルを選択してください",
		Options:     options,
		MinValues:   &[]int{1}[0],
		MaxValues:   1,
	}

	// メッセージを送信
	_, err = s.ChannelMessageSendComplex(m.ChannelID, &discordgo.MessageSend{
		Content: fmt.Sprintf("🔗 **両ボット接続 - ステップ 2/2**\n🔊 **スピーキングボット用チャンネル選択**\n\n✅ リスニングボット: `%s`\n\n次に、スピーキングボット（音声再生）を接続するボイスチャンネルを選択してください：", listeningChannelName),
		Components: []discordgo.MessageComponent{
			discordgo.ActionsRow{
				Components: []discordgo.MessageComponent{selectMenu},
			},
		},
	})

	if err != nil {
		log.Printf("[ListeningBot:%s] Error sending speaking channel selector: %v", guildID, err)
		s.ChannelMessageSend(m.ChannelID, "❌ **エラー**\nチャンネル選択メニューの表示に失敗しました。")
	}
}

// showEffectSelectorForConnect は、t!connect用のエフェクト選択メニューを表示します
func (b *ListeningBot) showEffectSelectorForConnect(s *discordgo.Session, i *discordgo.InteractionCreate, listeningChannelID string) {
	guildID := i.GuildID
	log.Printf("[ListeningBot:%s] Showing effect selector for connect, listeningChannelID: %s", guildID, listeningChannelID)

	// 音声エフェクト選択メニューを作成
	selectMenu := discordgo.SelectMenu{
		CustomID: fmt.Sprintf("audio_effect_select_connect_%s_%s_%s", guildID, i.Member.User.ID, listeningChannelID),
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
		Placeholder: "スピーキングボット用のエフェクトを選択してください",
	}

	log.Printf("[ListeningBot:%s] Created effect selector with CustomID: %s", guildID, selectMenu.CustomID)

	// フォローアップメッセージとしてエフェクト選択メニューを送信
	_, err := s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
		Content: "🔗 **両ボット接続 - ステップ 2/3**\n🎤 **エフェクト選択**\n\nスピーキングボット用の音声エフェクトを選択してください：",
		Components: []discordgo.MessageComponent{
			discordgo.ActionsRow{
				Components: []discordgo.MessageComponent{
					selectMenu,
				},
			},
		},
	})
	if err != nil {
		log.Printf("[ListeningBot:%s] Error sending effect selector: %v", guildID, err)

		// エフェクト選択メニューの表示に失敗した場合、デフォルトエフェクト（通常）でスピーキングチャンネル選択に進む
		log.Printf("[ListeningBot:%s] Falling back to default effect and showing speaking channel selector", guildID)

		// エラーメッセージを送信
		_, fallbackErr := s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
			Content: "⚠️ **エフェクト選択をスキップ**\nエフェクト選択メニューの表示に失敗したため、デフォルト（通常）エフェクトでスピーキングチャンネル選択に進みます。",
		})
		if fallbackErr != nil {
			log.Printf("[ListeningBot:%s] Error sending fallback message: %v", guildID, fallbackErr)
		}

		// デフォルトエフェクトでスピーキングチャンネル選択を表示
		// 元のメッセージ情報を再構築
		fakeMessage := &discordgo.MessageCreate{
			Message: &discordgo.Message{
				ChannelID: i.ChannelID,
				GuildID:   guildID,
				Author:    i.Member.User,
			},
		}
		b.showSpeakingChannelSelectorForConnect(s, fakeMessage, listeningChannelID, "通常")
	} else {
		log.Printf("[ListeningBot:%s] Successfully sent effect selector", guildID)
	}
}

// handleConnectEffectSelection は、t!connect用のエフェクト選択を処理します
func (b *ListeningBot) handleConnectEffectSelection(s *discordgo.Session, i *discordgo.InteractionCreate) {
	data := i.MessageComponentData()
	guildID := i.GuildID
	log.Printf("[ListeningBot:%s] Handling connect effect selection, CustomID: %s", guildID, data.CustomID)

	// 選択されたエフェクトを取得
	if len(data.Values) == 0 {
		log.Printf("[ListeningBot:%s] No effect values selected", guildID)
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "❌ **エラー**\nエフェクトが選択されていません。",
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
		return
	}

	selectedValue := data.Values[0]
	log.Printf("[ListeningBot:%s] Selected effect value: %s", guildID, selectedValue)
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

	log.Printf("[ListeningBot:%s] Mapped effect name: %s", guildID, effectName)

	// CustomIDからリスニングチャンネルIDを抽出
	// CustomID形式: "audio_effect_select_connect_guildID_userID_listeningChannelID"
	customIDParts := strings.Split(data.CustomID, "_")
	if len(customIDParts) < 6 {
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "❌ **エラー**\n無効なCustomID形式です。",
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
		return
	}

	listeningChannelID := customIDParts[5] // audio_effect_select_connect_guildID_userID_listeningChannelID

	// インタラクションに応答
	err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseUpdateMessage,
		Data: &discordgo.InteractionResponseData{
			Content:    fmt.Sprintf("✅ **エフェクト選択完了**\n選択されたエフェクト: **%s**", effectName),
			Components: []discordgo.MessageComponent{}, // セレクトメニューを削除
		},
	})
	if err != nil {
		log.Printf("[ListeningBot:%s] Error responding to interaction: %v", guildID, err)
		return
	}

	// スピーキングチャンネル選択メニューを表示
	// 元のメッセージ情報を再構築
	fakeMessage := &discordgo.MessageCreate{
		Message: &discordgo.Message{
			ChannelID: i.ChannelID,
			GuildID:   guildID,
			Author:    i.Member.User,
		},
	}

	b.showSpeakingChannelSelectorForConnect(s, fakeMessage, listeningChannelID, effectName)
}

// showSpeakingChannelSelectorForConnect は、t!connect用のスピーキングチャンネル選択メニューを表示します
func (b *ListeningBot) showSpeakingChannelSelectorForConnect(s *discordgo.Session, m *discordgo.MessageCreate, listeningChannelID, effectName string) {
	guildID := m.GuildID

	// ギルド情報を取得
	guild, err := s.State.Guild(guildID)
	if err != nil {
		log.Printf("[ListeningBot:%s] Error getting guild: %v", guildID, err)
		s.ChannelMessageSend(m.ChannelID, "❌ **エラー**\nサーバー情報の取得に失敗しました。")
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
		s.ChannelMessageSend(m.ChannelID, "❌ **エラー**\nこのサーバーにはボイスチャンネルがありません。")
		return
	}

	// リスニングチャンネル名を取得
	var listeningChannelName string
	for _, channel := range voiceChannels {
		if channel.ID == listeningChannelID {
			listeningChannelName = channel.Name
			break
		}
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

		// リスニングチャンネルと同じ場合は表示を変更
		if channel.ID == listeningChannelID {
			description += " (リスニングと同じ)"
		}

		options = append(options, discordgo.SelectMenuOption{
			Label:       channel.Name,
			Value:       fmt.Sprintf("connect_speaking_%s_%s_%s", listeningChannelID, channel.ID, effectName),
			Description: description,
			Emoji: &discordgo.ComponentEmoji{
				Name: "🔊",
			},
		})
	}

	// セレクトメニューコンポーネントを作成
	selectMenu := discordgo.SelectMenu{
		CustomID:    fmt.Sprintf("channel_select_connect_speaking_%s_%s_%s_%s", guildID, m.Author.ID, listeningChannelID, effectName),
		Placeholder: "スピーキングボットを接続するチャンネルを選択してください",
		Options:     options,
		MinValues:   &[]int{1}[0],
		MaxValues:   1,
	}

	// メッセージを送信
	_, err = s.ChannelMessageSendComplex(m.ChannelID, &discordgo.MessageSend{
		Content: fmt.Sprintf("🔗 **両ボット接続 - ステップ 3/3**\n🔊 **スピーキングボット用チャンネル選択**\n\n✅ リスニングボット: `%s`\n✅ エフェクト: `%s`\n\n最後に、スピーキングボット（音声再生）を接続するボイスチャンネルを選択してください：", listeningChannelName, effectName),
		Components: []discordgo.MessageComponent{
			discordgo.ActionsRow{
				Components: []discordgo.MessageComponent{selectMenu},
			},
		},
	})

	if err != nil {
		log.Printf("[ListeningBot:%s] Error sending speaking channel selector: %v", guildID, err)
		s.ChannelMessageSend(m.ChannelID, "❌ **エラー**\nチャンネル選択メニューの表示に失敗しました。")
	}
}

// HandleDisconnectCommand は、disconnectコマンドを処理します
func (b *ListeningBot) HandleDisconnectCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
	guildID := m.GuildID

	// ボイス接続を切断
	if conn, exists := b.voiceConn[guildID]; exists {
		log.Printf("[ListeningBot:%s] Disconnecting voice connection", guildID)
		conn.Disconnect()
		delete(b.voiceConn, guildID)
		b.cleanupGuildResources(guildID)

		s.ChannelMessageSend(m.ChannelID, "🔇 **ListeningBot切断完了**\n音声の受信を停止しました。")
	} else {
		s.ChannelMessageSend(m.ChannelID, "❌ **エラー**\nListeningBotは接続していません。")
	}

	// SpeakingBotも切断
	if b.speakingBot != nil && b.speakingBot.HasConnection(guildID) {
		log.Printf("[ListeningBot:%s] Also disconnecting SpeakingBot", guildID)
		b.speakingBot.handleDisconnectCommand(s, m)
	}
}

// connectToVoiceChannel は、ユーザーのボイスチャンネルに接続します
func (b *ListeningBot) connectToVoiceChannel(s *discordgo.Session, guildID, userID, textChannelID string) {
	// ユーザーのボイス状態を取得
	guild, err := s.State.Guild(guildID)
	if err != nil {
		log.Printf("[ListeningBot:%s] Error getting guild: %v", guildID, err)
		return
	}

	var channelID string
	for _, vs := range guild.VoiceStates {
		if vs.UserID == userID {
			channelID = vs.ChannelID
			break
		}
	}

	if channelID == "" {
		log.Printf("[ListeningBot:%s] User %s is not in a voice channel", guildID, userID)
		s.ChannelMessageSend(textChannelID, "❌ **エラー**\nボイスチャンネルに参加してからコマンドを実行してください。")
		return
	}

	// ボイスチャンネルに接続
	success := b.ConnectToVoiceChannelByID(s, guildID, channelID)
	if success {
		s.ChannelMessageSend(textChannelID, "✅ **ListeningBot接続完了**\n音声の受信を開始しました！")
		log.Printf("[ListeningBot:%s] Successfully connected to voice channel %s", guildID, channelID)
	} else {
		s.ChannelMessageSend(textChannelID, "❌ **接続エラー**\nボイスチャンネルへの接続に失敗しました。")
		log.Printf("[ListeningBot:%s] Failed to connect to voice channel %s", guildID, channelID)
	}
}

// ConnectToVoiceChannelByID は、指定されたボイスチャンネルIDに接続します
func (b *ListeningBot) ConnectToVoiceChannelByID(s *discordgo.Session, guildID, channelID string) bool {
	log.Printf("[ListeningBot:%s] Connecting to voice channel %s", guildID, channelID)

	vc, err := s.ChannelVoiceJoin(guildID, channelID, true, false)
	if err != nil {
		log.Printf("[ListeningBot:%s] Error joining voice channel: %v", guildID, err)
		return false
	}

	b.voiceConn[guildID] = vc

	// 音声受信ハンドラーを設定
	vc.AddHandler(b.voicePacketHandler)

	// 音声受信ループを開始
	go b.opusReceiveLoop(vc)

	log.Printf("[ListeningBot:%s] Connected to voice channel successfully", guildID)
	return true
}

// voicePacketHandler は、音声パケットを処理します
func (b *ListeningBot) voicePacketHandler(vc *discordgo.VoiceConnection, vs *discordgo.VoiceSpeakingUpdate) {
	// 音声受信の準備（必要に応じて）
}

// opusReceiveLoop は、Opus音声データを受信し続けるループです
func (b *ListeningBot) opusReceiveLoop(vc *discordgo.VoiceConnection) {
	log.Printf("[ListeningBot:%s] Starting Opus receive loop", vc.GuildID)

	for {
		select {
		case <-vc.OpusRecv:
			// チャネルが閉じられた場合、ループを終了
			log.Printf("[ListeningBot:%s] OpusRecv channel closed, stopping receive loop", vc.GuildID)
			return
		case packet, ok := <-vc.OpusRecv:
			if !ok {
				log.Printf("[ListeningBot:%s] OpusRecv channel closed, stopping receive loop", vc.GuildID)
				return
			}

			// Opusデータをデコード
			decoder, err := b.getOrCreateDecoder(packet.SSRC)
			if err != nil {
				log.Printf("[ListeningBot:%s] Error getting decoder for SSRC %d: %v", vc.GuildID, packet.SSRC, err)
				continue
			}

			pcmData, err := decoder.Decode(packet.Opus, PCMFrameSize, false)
			if err != nil {
				log.Printf("[ListeningBot:%s] Error decoding Opus data from SSRC %d: %v", vc.GuildID, packet.SSRC, err)
				continue
			}

			// PCMデータをミキサーに追加
			b.audioMixer.AddPCMData(vc.GuildID, packet.SSRC, pcmData)
		}
	}
}

// getOrCreateDecoder は、指定されたSSRCのデコーダーを取得または作成します
func (b *ListeningBot) getOrCreateDecoder(ssrc uint32) (*gopus.Decoder, error) {
	b.decodersMutex.Lock()
	defer b.decodersMutex.Unlock()

	if decoder, exists := b.decoders[ssrc]; exists {
		return decoder, nil
	}

	// 新しいデコーダーを作成
	decoder, err := gopus.NewDecoder(OpusSampleRate, OpusChannels)
	if err != nil {
		return nil, err
	}

	b.decoders[ssrc] = decoder
	log.Printf("[ListeningBot] Created new decoder for SSRC %d", ssrc)
	return decoder, nil
}

// cleanupGuildResources は、指定されたギルドのリソースをクリーンアップします
func (b *ListeningBot) cleanupGuildResources(guildID string) {
	b.audioMixer.CleanupGuildResources(guildID)
}

// cleanupUserResources は、指定されたSSRCのリソースをクリーンアップします
func (b *ListeningBot) cleanupUserResources(ssrcs ...uint32) {
	b.decodersMutex.Lock()
	defer b.decodersMutex.Unlock()

	for _, ssrc := range ssrcs {
		delete(b.decoders, ssrc)
		log.Printf("[ListeningBot] Cleaned up decoder for SSRC %d", ssrc)
	}
}

// cleanupAllResources は、すべてのリソースをクリーンアップします
func (b *ListeningBot) cleanupAllResources() {
	// デコーダーをクリーンアップ
	b.decodersMutex.Lock()
	b.decoders = make(map[uint32]*gopus.Decoder)
	b.decodersMutex.Unlock()

	// ミキサーリソースをクリーンアップ
	b.audioMixer.CleanupAllResources()

	log.Println("[ListeningBot] All resources cleaned up")
}
