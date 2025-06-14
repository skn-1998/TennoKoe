package main

import (
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"syscall"
	"time"

	"tennokoe-bot/internal/bot"
	"tennokoe-bot/internal/config"
)

// setupLogging は、ログファイルとコンソール両方への出力を設定します
func setupLogging() (*os.File, error) {
	// logsディレクトリを作成
	logsDir := "logs"
	if err := os.MkdirAll(logsDir, 0755); err != nil {
		return nil, fmt.Errorf("logsディレクトリの作成に失敗: %v", err)
	}

	// 古いログファイルを削除（7日以上前のファイル）
	cleanupOldLogs(logsDir)

	// 現在の日時でファイル名を生成
	now := time.Now()
	logFileName := fmt.Sprintf("tennokoe-bot-%s.log", now.Format("2006-01-02_15-04-05"))
	logFilePath := filepath.Join(logsDir, logFileName)

	// ログファイルを開く
	logFile, err := os.OpenFile(logFilePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if err != nil {
		return nil, fmt.Errorf("ログファイルの作成に失敗: %v", err)
	}

	// コンソールとファイル両方に出力するMultiWriterを設定
	multiWriter := io.MultiWriter(os.Stdout, logFile)
	log.SetOutput(multiWriter)

	// ログフォーマットを設定（日時、ファイル名、行番号を含む）
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds | log.Lshortfile)

	log.Printf("[SYSTEM] ログファイルを開始: %s", logFilePath)
	return logFile, nil
}

// cleanupOldLogs は、7日以上前のログファイルを削除します
func cleanupOldLogs(logsDir string) {
	files, err := filepath.Glob(filepath.Join(logsDir, "tennokoe-bot-*.log"))
	if err != nil {
		return
	}

	cutoffTime := time.Now().AddDate(0, 0, -7) // 7日前

	for _, file := range files {
		if info, err := os.Stat(file); err == nil {
			if info.ModTime().Before(cutoffTime) {
				os.Remove(file)
				log.Printf("[SYSTEM] 古いログファイルを削除: %s", file)
			}
		}
	}
}

// recoverFromPanic は、パニック発生時にスタックトレースをログに記録します
func recoverFromPanic() {
	if r := recover(); r != nil {
		log.Printf("[PANIC] アプリケーションがパニックしました: %v", r)
		log.Printf("[PANIC] スタックトレース:\n%s", string(debug.Stack()))

		// 少し待機してログが書き込まれるのを確実にする
		time.Sleep(1 * time.Second)

		// パニックを再度発生させて正常な終了処理を行う
		panic(r)
	}
}

func main() {
	// パニック回復を設定
	defer recoverFromPanic()

	log.Println("[SYSTEM] 音声転送システムを起動しています...")

	// ログファイル出力を設定
	logFile, err := setupLogging()
	if err != nil {
		log.Fatalf("[ERROR] ログ設定に失敗: %v", err)
	}
	defer logFile.Close()

	log.Println("[SYSTEM] ログファイル出力を開始しました")

	// 設定を読み込む
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("[ERROR] 設定の読み込みに失敗しました: %v", err)
	}
	log.Println("[CONFIG] 設定ファイルを読み込みました")

	// 音声プロセッサを作成
	// audioProcessor := audio.NewAudioProcessor() // 不要になった

	// リスニングBotを作成
	log.Println("[SYSTEM] リスニングBotを作成中...")
	listeningBot, err := bot.NewListeningBot(cfg.ListeningBotToken)
	if err != nil {
		log.Fatalf("[ERROR] リスニングBotの作成に失敗しました: %v", err)
	}

	// スピーキングBotを作成
	log.Println("[SYSTEM] スピーキングBotを作成中...")
	speakingBot, err := bot.NewSpeakingBot(cfg.SpeakingBotToken, listeningBot) // ★ listeningBot を渡す
	if err != nil {
		log.Fatalf("[ERROR] スピーキングBotの作成に失敗しました: %v", err)
	}

	// 相互参照を設定
	listeningBot.SetSpeakingBot(speakingBot)

	// Botを起動
	log.Println("[SYSTEM] リスニングBotを起動中...")
	err = listeningBot.Start()
	if err != nil {
		log.Fatalf("[ERROR] リスニングBotの起動に失敗しました: %v", err)
	}

	log.Println("[SYSTEM] スピーキングBotを起動中...")
	err = speakingBot.Start()
	if err != nil {
		log.Fatalf("[ERROR] スピーキングBotの起動に失敗しました: %v", err)
	}

	log.Println("[SYSTEM] 両方のBotが起動しました。Ctrl+Cで終了します。")

	// システム情報をログに記録
	if buildInfo, ok := debug.ReadBuildInfo(); ok {
		log.Printf("[INFO] Go version: %s", buildInfo.GoVersion)
		log.Printf("[INFO] Main module: %s", buildInfo.Main.Path)
	}
	log.Printf("[INFO] Process PID: %d", os.Getpid())

	// シグナルを待機
	sc := make(chan os.Signal, 1)
	signal.Notify(sc, syscall.SIGINT, syscall.SIGTERM, os.Interrupt)
	<-sc

	// クリーンアップ
	log.Println("[SYSTEM] プログラムを終了します...")
	log.Println("[SYSTEM] リスニングBotを停止中...")
	listeningBot.Stop()
	log.Println("[SYSTEM] スピーキングBotを停止中...")
	speakingBot.Stop()

	log.Println("[SYSTEM] プログラムが正常に終了しました")

	// ログファイルの最終フラッシュを確実に行う
	if logFile != nil {
		logFile.Sync()
	}

	// audioProcessor.Close() // 不要になった
}
