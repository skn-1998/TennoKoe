package config

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/joho/godotenv"
)

// Config は、アプリケーションの設定を保持する構造体です
type Config struct {
	ListeningBotToken string
	SpeakingBotToken  string
	TempDir           string
	AudioPipeFile     string
}

// Load は、環境変数から設定を読み込みます
func Load() (*Config, error) {
	// .envファイルを読み込む
	err := godotenv.Load()
	if err != nil {
		return nil, errors.New("Error loading .env file: " + err.Error())
	}

	// 必須の環境変数を取得
	listeningBotToken := os.Getenv("LISTENING_BOT_TOKEN")
	if listeningBotToken == "" {
		return nil, errors.New("LISTENING_BOT_TOKEN is not set")
	}

	speakingBotToken := os.Getenv("SPEAKING_BOT_TOKEN")
	if speakingBotToken == "" {
		return nil, errors.New("SPEAKING_BOT_TOKEN is not set")
	}

	// 一時ディレクトリの設定
	tempDir := filepath.Join(".", "temp")
	if _, err := os.Stat(tempDir); os.IsNotExist(err) {
		err = os.MkdirAll(tempDir, 0755)
		if err != nil {
			return nil, errors.New("Failed to create temp directory: " + err.Error())
		}
	}

	// 音声パイプファイルのパス
	audioPipeFile := filepath.Join(tempDir, "audio_pipe.pcm")

	return &Config{
		ListeningBotToken: listeningBotToken,
		SpeakingBotToken:  speakingBotToken,
		TempDir:           tempDir,
		AudioPipeFile:     audioPipeFile,
	}, nil
}
