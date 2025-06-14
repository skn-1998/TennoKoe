package audio

import (
	"log"
	"sync"
)

// AudioProcessor は、音声データの処理と転送を担当します
type AudioProcessor struct {
	mutex     sync.Mutex
	listeners []chan []byte
}

// NewAudioProcessor は、新しいAudioProcessorインスタンスを作成します
func NewAudioProcessor() *AudioProcessor {
	return &AudioProcessor{
		listeners: make([]chan []byte, 0),
	}
}

// AddListener は、音声データを受信するリスナーを追加します
func (p *AudioProcessor) AddListener(listener chan []byte) {
	p.mutex.Lock()
	defer p.mutex.Unlock()
	p.listeners = append(p.listeners, listener)
	log.Printf("[AudioProcessor] Added listener. Total listeners: %d", len(p.listeners))
}

// RemoveListener は、リスナーを削除します
func (p *AudioProcessor) RemoveListener(listener chan []byte) {
	p.mutex.Lock()
	defer p.mutex.Unlock()
	initialLen := len(p.listeners)
	for i, l := range p.listeners {
		if l == listener {
			p.listeners = append(p.listeners[:i], p.listeners[i+1:]...)
			log.Printf("[AudioProcessor] Removed listener. Total listeners: %d", len(p.listeners))
			break
		}
	}
	if initialLen == len(p.listeners) {
		log.Printf("[AudioProcessor] RemoveListener called but listener not found.")
	}
}

// ProcessAudio は、音声データを処理し、リスナーに送信します
func (p *AudioProcessor) ProcessAudio(data []byte) {
	// log.Printf("[AudioProcessor] ProcessAudio called with data size: %d", len(data)) // コメントアウト
	p.mutex.Lock()
	listeners := make([]chan []byte, len(p.listeners))
	copy(listeners, p.listeners)
	p.mutex.Unlock()

	// log.Printf("[AudioProcessor] Processing audio for %d listeners", len(listeners)) // コメントアウト

	// すべてのリスナーにデータを送信
	for i, listener := range listeners {
		dataCopy := make([]byte, len(data))
		copy(dataCopy, data)

		select {
		case listener <- dataCopy:
			// log.Printf("[AudioProcessor] Sent data (size: %d) to listener %d", len(dataCopy), i) // コメントアウト
		default:
			log.Printf("[AudioProcessor] Listener %d channel blocked, skipping data (size: %d)", i, len(dataCopy))
			// TODO: バッファが満杯の場合の処理を検討 (ログ出力、データの破棄など)
		}
	}
}

// Close は、AudioProcessorのリソースを解放します
func (p *AudioProcessor) Close() {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	// すべてのリスナーを閉じる
	for _, listener := range p.listeners {
		close(listener)
	}
	log.Printf("[AudioProcessor] Closed all listeners. Total listeners: %d", len(p.listeners))
	p.listeners = make([]chan []byte, 0)
}
