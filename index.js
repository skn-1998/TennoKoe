// 必要なモジュールをインポート
const { Client, GatewayIntentBits } = require('discord.js');
const { 
  joinVoiceChannel, 
  createAudioPlayer, 
  createAudioResource,
  VoiceConnectionStatus,
  AudioPlayerStatus,
  EndBehaviorType,
  getVoiceConnection,
  entersState,
  StreamType
} = require('@discordjs/voice');
const { Transform, PassThrough } = require('stream');
const prism = require('prism-media');
const fs = require('fs');
const path = require('path');
require('dotenv').config();

// 環境変数からトークンとチャンネルIDを取得
const LISTENING_BOT_TOKEN = process.env.LISTENING_BOT_TOKEN;
const SPEAKING_BOT_TOKEN = process.env.SPEAKING_BOT_TOKEN;

// 一時的な音声データを保存するディレクトリ
const TEMP_DIR = path.join(__dirname, 'temp');
if (!fs.existsSync(TEMP_DIR)) {
  fs.mkdirSync(TEMP_DIR);
}

// 音声データを一時的に保存するためのファイルパス
const AUDIO_PIPE_FILE = path.join(TEMP_DIR, 'audio_pipe.pcm');

// グローバルな音声データストリーム（インメモリ）
// 注意: この実装は単一の音声転送セッションを想定しています。
// 複数のGuildや転送を同時に扱う場合は、Guildごと/セッションごとにストリームを管理する必要があります。
const audioStream = new PassThrough();

// リスニング用Botのクライアント作成
const listeningBot = new Client({
  intents: [
    GatewayIntentBits.Guilds,
    GatewayIntentBits.GuildVoiceStates,
    GatewayIntentBits.GuildMessages,
    GatewayIntentBits.MessageContent
  ]
});

// スピーキング用Botのクライアント作成
const speakingBot = new Client({
  intents: [
    GatewayIntentBits.Guilds,
    GatewayIntentBits.GuildVoiceStates,
    GatewayIntentBits.GuildMessages,
    GatewayIntentBits.MessageContent
  ]
});

// 音声データを処理するためのTransformストリーム
class AudioProcessor extends Transform {
  constructor(options) {
    super(options);
  }

  _transform(chunk, encoding, callback) {
    // ここで必要に応じて音声データを処理できます
    // 例: 音量調整、ノイズ除去など
    this.push(chunk);
    callback();
  }
}

// リスニングBotの初期化
listeningBot.once('ready', async () => {
  console.log(`リスニングBot (${listeningBot.user.tag}) が起動しました`);
});

// スピーキングBotの初期化
speakingBot.once('ready', async () => {
  console.log(`スピーキングBot (${speakingBot.user.tag}) が起動しました`);
});

// heaven!listenコマンドの処理
listeningBot.on('messageCreate', async (message) => {
  // listeningBot自身のメッセージや他のBotのメッセージは無視
  if (message.author.id === listeningBot.user.id || message.author.bot) return;
  if (message.content.toLowerCase() !== 'heaven!listen') return;

  console.log(`[ListeningBot] Received command: ${message.content} from ${message.author.tag}`);

  try {
    const member = message.guild.members.cache.get(message.author.id);
    const voiceChannel = member.voice.channel;

    if (!voiceChannel) {
      return message.reply('ボイスチャンネルに参加してから実行してください。');
    }

    // スピーキングBotが同じチャンネルにいないか確認 (speakingBotのIDを指定)
    const speakingConnection = getVoiceConnection(message.guild.id, speakingBot.user.id);
    if (speakingConnection && speakingConnection.joinConfig.channelId === voiceChannel.id) {
        console.log('[ListeningBot] Speaking bot is already in the target channel.');
        return message.reply('スピーキングBotが既に同じチャンネルに接続されています。別のチャンネルを選択してください。');
    }

    // 既存のリスニング接続を確認して切断 (listeningBotのIDを指定)
    const existingListeningConnection = getVoiceConnection(message.guild.id, listeningBot.user.id);
    // 既に破棄済みまたは破棄中でないか確認
    if (existingListeningConnection && ![VoiceConnectionStatus.Destroyed, VoiceConnectionStatus.Destroying].includes(existingListeningConnection.state.status)) {
        console.log("[ListeningBot] Destroying existing listening connection.");
        existingListeningConnection.destroy();
        try {
            await entersState(existingListeningConnection, VoiceConnectionStatus.Destroyed, 5_000);
            console.log("[ListeningBot] Existing listening connection destroyed successfully.");
        } catch (error) {
            // タイムアウトした場合など、すでに Destroyed になっている可能性もある
            console.warn(`[ListeningBot] Failed to wait for existing connection destroy or already destroyed: ${error.message}`);
        }
    } else if (existingListeningConnection) {
        console.log("[ListeningBot] Existing listening connection already destroyed or destroying.");
    }

    console.log(`[ListeningBot] Joining voice channel: ${voiceChannel.name}`);
    // listeningBotとして接続
    console.log(`[ListeningBot DEBUG] Attempting joinVoiceChannel for guild ${message.guild.id}, channel ${voiceChannel.id} with bot ID ${listeningBot.user.id}`);
    const connection = joinVoiceChannel({
      channelId: voiceChannel.id,
      guildId: voiceChannel.guild.id,
      adapterCreator: voiceChannel.guild.voiceAdapterCreator,
      selfDeaf: false, // チャンネルの音声を聞く
      selfMute: true   // 自分は喋らない
    });
    console.log(`[ListeningBot DEBUG] joinVoiceChannel called. Connection status: ${connection.state.status}`);

    let receiver;
    let subscription;
    let decoder;

    connection.on(VoiceConnectionStatus.Ready, () => {
      console.log(`[ListeningBot] Connected to voice channel: ${voiceChannel.name}`);
      message.reply(`${voiceChannel.name} に接続しました。`);

      receiver = connection.receiver;
      const userIds = message.guild.members.cache
          .filter(m => !m.user.bot && m.voice.channelId === voiceChannel.id)
          .map(m => m.id);

      if (userIds.length === 0) {
          console.log("[ListeningBot] No users to listen to.");
          connection.destroy(); // 誰もいなければ切断
          return;
      }

      console.log(`[ListeningBot] Listening to users: ${userIds.join(', ')}`);

      // ここでuserごとのsubscribe/decode/pipeが必要になる
      // 現状は一人目のみを処理
      userIds.forEach(userId => {
          console.log(`[ListeningBot] Subscribing to user ${userId}`);
          // ★注意：現状の実装では、複数のユーザーに対応できていません。
          // 最初のユーザーのみを処理します。
          if (!subscription) {
              subscription = receiver.subscribe(userId, {
                  end: { behavior: EndBehaviorType.Manual },
              });
              decoder = new prism.opus.Decoder({ rate: 48000, channels: 2, frameSize: 960 });
              subscription.pipe(decoder).pipe(audioStream, { end: false });

              subscription.on('error', (error) => {
                console.error(`[ListeningBot] Subscription error for user ${userId}:`, error);
                if (decoder) decoder.destroy();
                if (subscription) subscription.destroy();
                // pipe解除も必要なら行う
              });
              decoder.on('error', (error) => {
                console.error(`[ListeningBot] Decoder error for user ${userId}:`, error);
                if (subscription) subscription.destroy();
                 // pipe解除も必要なら行う
              });
          } else {
              console.warn(`[ListeningBot] Currently only supports listening to one user. Skipping ${userId}.`);
          }
      });

    });

    connection.on(VoiceConnectionStatus.Disconnected, async () => {
      console.log('[ListeningBot] Disconnected.');
      // クリーンアップ前に存在確認と状態確認
      // subscription や decoder の 'destroyed' プロパティの有無はライブラリによるため、一旦 try-catch で囲む
      if (subscription) {
         try { if (typeof subscription.destroy === 'function') subscription.destroy(); } catch (e) { console.warn("Error destroying subscription:", e.message); }
         subscription = null; // 参照削除
      }
      if (decoder) {
         try { if (typeof decoder.destroy === 'function') decoder.destroy(); } catch (e) { console.warn("Error destroying decoder:", e.message); }
         decoder = null; // 参照削除
      }
    });

    connection.on(VoiceConnectionStatus.Destroyed, () => {
        console.log('[ListeningBot] Connection destroyed.');
       // クリーンアップ前に存在確認と状態確認
       if (subscription) {
          try { if (typeof subscription.destroy === 'function') subscription.destroy(); } catch (e) { console.warn("Error destroying subscription:", e.message); }
          subscription = null;
       }
       if (decoder) {
          try { if (typeof decoder.destroy === 'function') decoder.destroy(); } catch (e) { console.warn("Error destroying decoder:", e.message); }
          decoder = null;
       }
    });

  } catch (error) {
    console.error('[ListeningBot] Voice channel connection error:', error);
    message.reply('ボイスチャンネルへの接続に失敗しました。');
  }
});

// heaven!speakコマンドの処理
speakingBot.on('messageCreate', async (message) => {
  // speakingBot自身のメッセージや他のBotのメッセージは無視
  if (message.author.id === speakingBot.user.id || message.author.bot) return;
  if (message.content.toLowerCase() !== 'heaven!speak') return;

  console.log(`[SpeakingBot] Received command: ${message.content} from ${message.author.tag}`);

  try {
    const member = message.guild.members.cache.get(message.author.id);
    const voiceChannel = member.voice.channel;

    if (!voiceChannel) {
      return message.reply('ボイスチャンネルに参加してから実行してください。');
    }

    // リスニングBotが同じチャンネルにいないか確認 (listeningBotのIDを指定)
    const listeningConnection = getVoiceConnection(message.guild.id, listeningBot.user.id);
    if (listeningConnection && listeningConnection.joinConfig.channelId === voiceChannel.id) {
        console.log('[SpeakingBot] Listening bot is already in the target channel.');
        return message.reply('リスニングBotが既に同じチャンネルに接続されています。別のチャンネルを選択してください。');
    }

    // 既存のスピーキング接続を確認して切断 (speakingBotのIDを指定)
    const existingSpeakingConnection = getVoiceConnection(message.guild.id, speakingBot.user.id);
     // 既に破棄済みまたは破棄中でないか確認
    if (existingSpeakingConnection && ![VoiceConnectionStatus.Destroyed, VoiceConnectionStatus.Destroying].includes(existingSpeakingConnection.state.status)) {
        console.log("[SpeakingBot] Destroying existing speaking connection.");
        existingSpeakingConnection.destroy();
         try {
            await entersState(existingSpeakingConnection, VoiceConnectionStatus.Destroyed, 5_000);
             console.log("[SpeakingBot] Existing speaking connection destroyed successfully.");
        } catch (error) {
            console.warn(`[SpeakingBot] Failed to wait for existing connection destroy or already destroyed: ${error.message}`);
        }
    } else if (existingSpeakingConnection) {
         console.log("[SpeakingBot] Existing speaking connection already destroyed or destroying.");
    }

    console.log(`[SpeakingBot] Joining voice channel: ${voiceChannel.name}`);
    // speakingBotとして接続
    console.log(`[SpeakingBot DEBUG] Attempting joinVoiceChannel for guild ${message.guild.id}, channel ${voiceChannel.id} with bot ID ${speakingBot.user.id}`);
    const connection = joinVoiceChannel({
      channelId: voiceChannel.id,
      guildId: voiceChannel.guild.id,
      adapterCreator: voiceChannel.guild.voiceAdapterCreator,
      selfDeaf: true,  // 自分は聞かない
      selfMute: false  // 自分は喋る
    });
    console.log(`[SpeakingBot DEBUG] joinVoiceChannel called. Connection status: ${connection.state.status}`);

    let player;

    connection.on(VoiceConnectionStatus.Ready, () => {
      console.log(`[SpeakingBot] Connected to voice channel: ${voiceChannel.name}`);
      message.reply(`${voiceChannel.name} に接続しました。`);

      player = createAudioPlayer();
      connection.subscribe(player);

      const audioResource = createAudioResource(audioStream, {
        inputType: StreamType.Raw,
      });

      player.play(audioResource);

      player.on(AudioPlayerStatus.Idle, () => {
        console.log('[SpeakingBot] Player is idle (waiting for data or stream ended).');
      });

      player.on('error', error => {
        console.error('[SpeakingBot] Player error:', error.message);
        if (player) player.stop();
      });

      // audioStream側のイベントリスナーは一度だけ設定するのが望ましい
      // ここで毎回設定するとリスナーが増え続ける可能性がある
      // 必要ならグローバルスコープや接続管理オブジェクトで管理する
      /*
      audioStream.once('end', () => { ... });
      audioStream.once('error', (err) => { ... });
      */

    });

    connection.on(VoiceConnectionStatus.Disconnected, async () => {
      console.log('[SpeakingBot] Disconnected.');
      // クリーンアップ前に存在確認とPlayerの状態確認
      if (player && player.state.status !== AudioPlayerStatus.Idle) {
           try {
               player.stop(true); // trueでリソースを強制的に停止・破棄できるか試す (API仕様による)
               console.log("[SpeakingBot] Player stopped on disconnect.");
            } catch (e) {
                console.warn("Error stopping player on disconnect:", e.message);
            }
      }
    });

     connection.on(VoiceConnectionStatus.Destroyed, () => {
        console.log('[SpeakingBot] Connection destroyed.');
         // クリーンアップ前に存在確認とPlayerの状態確認
        if (player && player.state.status !== AudioPlayerStatus.Idle) {
             try {
                 player.stop(true);
                 console.log("[SpeakingBot] Player stopped on destroy.");
             } catch (e) {
                 console.warn("Error stopping player on destroy:", e.message);
             }
        }
        // connection.unsubscribe(); // APIにあれば試す価値あり
        player = null; // 参照を削除してGC対象にする
    });

  } catch (error) {
    console.error('[SpeakingBot] Voice channel connection error:', error);
    message.reply('ボイスチャンネルへの接続に失敗しました。');
  }
});

// エラーハンドリング
listeningBot.on('error', console.error);
speakingBot.on('error', console.error);

// Botのログイン
listeningBot.login(LISTENING_BOT_TOKEN);
speakingBot.login(SPEAKING_BOT_TOKEN);

// audioStreamのグローバルなエラー/終了ハンドリング (一度だけ設定)
audioStream.once('end', () => {
    console.log('[Global] audioStream ended.');
    // 必要ならここで関連リソースをクリーンアップ
});
audioStream.once('error', (err) => {
    console.error('[Global] audioStream error:', err.message);
    // 必要ならここで関連リソースをクリーンアップ
});

// プロセス終了時のクリーンアップ
process.on('SIGINT', () => {
  console.log('プログラムを終了します...');
  const adaptersToDestroy = [];

  listeningBot.voice.adapters.forEach((adapter, guildId) => {
      const connection = getVoiceConnection(guildId, listeningBot.user.id);
      // 破棄中でない、かつ破棄済みでない接続のみ対象
      if (connection && ![VoiceConnectionStatus.Destroyed, VoiceConnectionStatus.Destroying].includes(connection.state.status)) {
          console.log(`[SIGINT] Scheduling listening bot connection destroy in guild ${guildId}...`);
          adaptersToDestroy.push(connection.destroy.bind(connection)); // destroyメソッドをbindして追加
      }
  });
  speakingBot.voice.adapters.forEach((adapter, guildId) => {
      const connection = getVoiceConnection(guildId, speakingBot.user.id);
       // 破棄中でない、かつ破棄済みでない接続のみ対象
       if (connection && ![VoiceConnectionStatus.Destroyed, VoiceConnectionStatus.Destroying].includes(connection.state.status)) {
          console.log(`[SIGINT] Scheduling speaking bot connection destroy in guild ${guildId}...`);
           adaptersToDestroy.push(connection.destroy.bind(connection));
      }
  });

  // 集めたdestroy処理を実行
  adaptersToDestroy.forEach(destroyFn => {
      try { destroyFn(); } catch(e) { console.warn("[SIGINT] Error destroying connection:", e.message); }
  });

  console.log("[SIGINT] Closing audio stream...");
  if (audioStream && !audioStream.destroyed) { // ストリームの状態も確認
    audioStream.end();
  }

  console.log("[SIGINT] Destroying Discord clients...");
  if (listeningBot) listeningBot.destroy();
  if (speakingBot) speakingBot.destroy();

  console.log("[SIGINT] Exiting process.");
  // 少し待ってから終了する (非同期処理の完了を期待)
  setTimeout(() => process.exit(0), 1000);
});

console.log('音声転送システムを起動しています...');
