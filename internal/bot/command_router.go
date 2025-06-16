package bot

import (
	"fmt"
	"log"
	"strings"

	"github.com/bwmarrin/discordgo"
)

// CommandRouter は、チャットコマンドをルーティングするための構造体です
type CommandRouter struct {
	listeningBot *ListeningBot
	speakingBot  *SpeakingBot
}

// NewCommandRouter は、新しいCommandRouterを作成します
func NewCommandRouter(listeningBot *ListeningBot, speakingBot *SpeakingBot) *CommandRouter {
	router := &CommandRouter{
		listeningBot: listeningBot,
		speakingBot:  speakingBot,
	}

	// インタラクションハンドラーはListeningBotとSpeakingBot自体に任せる
	// 重複を避けるため、CommandRouter独自のハンドラーは登録しない

	return router
}

// HandleMessage は、受信したメッセージを適切なコマンドハンドラーにルーティングします
func (cr *CommandRouter) HandleMessage(s *discordgo.Session, m *discordgo.MessageCreate) {
	// 自分自身のメッセージやボットのメッセージは無視
	if m.Author.ID == s.State.User.ID || m.Author.Bot {
		return
	}

	// コマンドでない場合は無視
	if !strings.HasPrefix(m.Content, "t!") {
		return
	}

	// コマンドを小文字に変換
	command := strings.ToLower(m.Content)

	log.Printf("[CommandRouter] Received command: %s from %s", command, m.Author.Username)

	// レシーバー経由で両方のBotのセッションにアクセス
	listeningSession := cr.listeningBot.GetSession()
	speakingSession := cr.speakingBot.GetSession()

	// 現在のセッションがどちらのBotから来たかを判定
	var isFromListeningBot, isFromSpeakingBot bool
	if s.State.User.ID == listeningSession.State.User.ID {
		isFromListeningBot = true
		log.Printf("[CommandRouter] Command received from ListeningBot session")
	} else if s.State.User.ID == speakingSession.State.User.ID {
		isFromSpeakingBot = true
		log.Printf("[CommandRouter] Command received from SpeakingBot session")
	} else {
		log.Printf("[CommandRouter] Command received from unknown session")
		return // 不明なセッションからのコマンドは無視
	}

	// コマンドに応じてルーティング（両方のセッションとメッセージにアクセス可能）
	switch command {
	case "t!listen":
		cr.handleListenCommandWithBothSessions(listeningSession, speakingSession, m, isFromListeningBot, isFromSpeakingBot)
	case "t!speak":
		cr.handleSpeakCommandWithBothSessions(listeningSession, speakingSession, m, isFromListeningBot, isFromSpeakingBot)
	case "t!disconnect":
		cr.handleDisconnectCommandWithBothSessions(listeningSession, speakingSession, m, isFromListeningBot, isFromSpeakingBot)
	case "t!connect":
		log.Printf("[CommandRouter] Routing t!connect to channel selector")
		cr.handleConnectCommandWithBothSessions(listeningSession, speakingSession, m, isFromListeningBot, isFromSpeakingBot)

	default:
		cr.handleUnknownCommandWithBothSessions(listeningSession, speakingSession, m, isFromListeningBot, isFromSpeakingBot)
	}
}

func (cr *CommandRouter) handleConnectCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
	log.Printf("[CommandRouter] Showing channel selector for t!connect")
	cr.showConnectChannelSelector(s, m, cr.listeningBot, cr.speakingBot)
}

// showConnectChannelSelector は、t!connect用のボイスチャンネル選択メニューを表示します（リスニングボット用）
func (cr *CommandRouter) showConnectChannelSelector(s *discordgo.Session, m *discordgo.MessageCreate, listeningBot *ListeningBot, speakingBot *SpeakingBot) {
	cr.showListeningChannelSelector(s, m, listeningBot, speakingBot)
}

// showListeningChannelSelector は、リスニングボット用のチャンネル選択メニューを表示します
func (cr *CommandRouter) showListeningChannelSelector(s *discordgo.Session, m *discordgo.MessageCreate, listeningBot *ListeningBot, speakingBot *SpeakingBot) {
	guildID := m.GuildID

	// ギルド情報を取得
	guild, err := s.State.Guild(guildID)
	if err != nil {
		log.Printf("[CommandRouter:%s] Error getting guild: %v", guildID, err)
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
			Value:       fmt.Sprintf("connect_listening_%s", channel.ID),
			Description: description,
			Emoji: &discordgo.ComponentEmoji{
				Name: "🎤",
			},
		})
	}

	// セレクトメニューコンポーネントを作成
	selectMenu := discordgo.SelectMenu{
		CustomID:    fmt.Sprintf("channel_select_connect_listening_%s_%s", guildID, m.Author.ID),
		Placeholder: "リスニングボットを接続するチャンネルを選択してください",
		Options:     options,
		MinValues:   &[]int{1}[0],
		MaxValues:   1,
	}

	// メッセージを送信
	_, err = s.ChannelMessageSendComplex(m.ChannelID, &discordgo.MessageSend{
		Content: "🔗 **両ボット接続 - ステップ 1/2**\n🎤 **リスニングボット用チャンネル選択**\nまず、リスニングボット（音声受信）を接続するボイスチャンネルを選択してください：",
		Components: []discordgo.MessageComponent{
			discordgo.ActionsRow{
				Components: []discordgo.MessageComponent{selectMenu},
			},
		},
	})

	if err != nil {
		log.Printf("[CommandRouter:%s] Error sending listening channel selector: %v", guildID, err)
		s.ChannelMessageSend(m.ChannelID, "❌ **エラー**\nチャンネル選択メニューの表示に失敗しました。")
	}
}

// showSpeakingChannelSelector は、スピーキングボット用のチャンネル選択メニューを表示します
func (cr *CommandRouter) showSpeakingChannelSelector(s *discordgo.Session, m *discordgo.MessageCreate, listeningBot *ListeningBot, speakingBot *SpeakingBot, listeningChannelID string) {
	guildID := m.GuildID

	// ギルド情報を取得
	guild, err := s.State.Guild(guildID)
	if err != nil {
		log.Printf("[CommandRouter:%s] Error getting guild: %v", guildID, err)
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
		log.Printf("[CommandRouter:%s] Error sending speaking channel selector: %v", guildID, err)
		s.ChannelMessageSend(m.ChannelID, "❌ **エラー**\nチャンネル選択メニューの表示に失敗しました。")
	}
}

// handleListenCommand は、t!listenコマンドを処理します
func (cr *CommandRouter) handleListenCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
	log.Printf("[CommandRouter] Routing t!listen to ListeningBot")
	cr.listeningBot.HandleListenCommand(s, m)
}

// handleSpeakCommand は、t!speakコマンドを処理します
func (cr *CommandRouter) handleSpeakCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
	log.Printf("[CommandRouter] Routing t!speak to SpeakingBot")
	cr.speakingBot.handleSpeakCommand(s, m)
}

// handleDisconnectCommand は、t!disconnectコマンドを処理します
func (cr *CommandRouter) handleDisconnectCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
	log.Printf("[CommandRouter] Routing t!disconnect to ListeningBot")
	cr.listeningBot.HandleDisconnectCommand(s, m)
}

// handleUnknownCommand は、未知のコマンドを処理します
func (cr *CommandRouter) handleUnknownCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
	log.Printf("[CommandRouter] Unknown command: %s", m.Content)

	helpMessage := "❓ **未知のコマンドです**\n\n" +
		"**利用可能なコマンド:**\n" +
		"• `t!listen` - 音声受信ボットを起動\n" +
		"• `t!speak` - 音声再生ボットを起動（エフェクト選択あり）\n" +
		"• `t!connect` - 両方のボットを同時起動\n" +
		"• `t!disconnect` - 全ボットを切断\n\n" +
		"詳しい使い方については、各コマンドを実行してください。"

	s.ChannelMessageSend(m.ChannelID, helpMessage)
}

// handleListenCommandWithBothSessions は、t!listenコマンドを処理します
func (cr *CommandRouter) handleListenCommandWithBothSessions(listeningSession *discordgo.Session, speakingSession *discordgo.Session, m *discordgo.MessageCreate, isFromListeningBot, isFromSpeakingBot bool) {
	log.Printf("[CommandRouter] handleListenCommandWithBothSessions: Routing t!listen to ListeningBot")
	log.Printf("[CommandRouter] ListeningBot exists: %v", cr.listeningBot != nil)

	// メッセージを受信したセッション（リスニングボットのセッション）を使用
	if cr.listeningBot != nil {
		log.Printf("[CommandRouter] Calling ListeningBot.HandleListenCommand...")
		cr.listeningBot.HandleListenCommand(listeningSession, m)
		log.Printf("[CommandRouter] ListeningBot.HandleListenCommand completed")
	} else {
		log.Printf("[CommandRouter] ERROR: ListeningBot is nil!")
	}
}

// handleSpeakCommandWithBothSessions は、t!speakコマンドを処理します
func (cr *CommandRouter) handleSpeakCommandWithBothSessions(listeningSession *discordgo.Session, speakingSession *discordgo.Session, m *discordgo.MessageCreate, isFromListeningBot, isFromSpeakingBot bool) {
	log.Printf("[CommandRouter] Routing t!speak to SpeakingBot")
	cr.speakingBot.handleSpeakCommand(speakingSession, m)
}

// handleDisconnectCommandWithBothSessions は、t!disconnectコマンドを処理します
func (cr *CommandRouter) handleDisconnectCommandWithBothSessions(listeningSession *discordgo.Session, speakingSession *discordgo.Session, m *discordgo.MessageCreate, isFromListeningBot, isFromSpeakingBot bool) {
	log.Printf("[CommandRouter] Routing t!disconnect to ListeningBot")
	cr.listeningBot.HandleDisconnectCommand(listeningSession, m)
}

// handleUnknownCommandWithBothSessions は、未知のコマンドを処理します
func (cr *CommandRouter) handleUnknownCommandWithBothSessions(listeningSession *discordgo.Session, speakingSession *discordgo.Session, m *discordgo.MessageCreate, isFromListeningBot, isFromSpeakingBot bool) {
	log.Printf("[CommandRouter] Unknown command: %s", m.Content)

	helpMessage := "❓ **未知のコマンドです**\n\n" +
		"**利用可能なコマンド:**\n" +
		"• `t!listen` - 音声受信ボットを起動\n" +
		"• `t!speak` - 音声再生ボットを起動（エフェクト選択あり）\n" +
		"• `t!connect` - 両方のボットを同時起動\n" +
		"• `t!disconnect` - 全ボットを切断\n\n" +
		"詳しい使い方については、各コマンドを実行してください。"

	// どちらのセッションからでも応答できるようにする
	if isFromListeningBot {
		listeningSession.ChannelMessageSend(m.ChannelID, helpMessage)
	} else if isFromSpeakingBot {
		speakingSession.ChannelMessageSend(m.ChannelID, helpMessage)
	} else {
		// デフォルトはListeningSessionを使用
		listeningSession.ChannelMessageSend(m.ChannelID, helpMessage)
	}
}

// handleConnectCommandWithBothSessions は、t!connectコマンドを処理します
func (cr *CommandRouter) handleConnectCommandWithBothSessions(listeningSession *discordgo.Session, speakingSession *discordgo.Session, m *discordgo.MessageCreate, isFromListeningBot, isFromSpeakingBot bool) {
	log.Printf("[CommandRouter] Showing channel selector for t!connect")
	// どちらのセッションからでも応答できるようにしてチャンネル選択を開始
	if isFromListeningBot {
		cr.showConnectChannelSelector(listeningSession, m, cr.listeningBot, cr.speakingBot)
	} else if isFromSpeakingBot {
		cr.showConnectChannelSelector(speakingSession, m, cr.listeningBot, cr.speakingBot)
	} else {
		// デフォルトはListeningSessionを使用
		cr.showConnectChannelSelector(listeningSession, m, cr.listeningBot, cr.speakingBot)
	}
}
