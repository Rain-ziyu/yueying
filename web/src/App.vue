<script setup lang="ts">
import { computed, ref, onMounted, onUnmounted } from "vue";
import {
  ArrowRight,
  Check,
  Copy,
  Film,
  FolderOpen,
  LogOut,
  Maximize,
  MessageSquare,
  Pause,
  Play,
  RefreshCw,
  RotateCcw,
  RotateCw,
  Send,
  Upload as UploadIcon,
} from "@lucide/vue";
import {
  api,
  endpoint,
  configureEndpoint,
  upload,
  type Member,
  type UploadPhase,
} from "./api";
type State = {
  asset: string;
  position: number;
  rate: number;
  playing: boolean;
  version: number;
  at: number;
};
const serverUrl = ref(endpoint),
  configurable = !import.meta.env.VITE_API_URL;
const member = ref<Member | null>(null),
  name = ref(""),
  invite = ref(new URLSearchParams(location.search).get("room") || "");
const assets = ref<
    { id: string; name: string; transcode: string; detail: string }[]
  >([]),
  video = ref<HTMLVideoElement>(),
  stage = ref<HTMLElement>(),
  msg = ref(""),
  text = ref(""),
  status = ref(""),
  connected = ref(false),
  position = ref(0),
  duration = ref(0),
  wide = ref(false);
type Transfer = {
  name: string;
  size: number;
  sent: number;
  phase: UploadPhase | "上传完成" | "上传失败";
  error?: string;
};
const transfer = ref<Transfer | null>(null);
const busy = ref(false);
const percent = computed(() => {
  const t = transfer.value;
  return t?.size ? Math.floor((100 * t.sent) / t.size) : 0;
});
let uploadController: AbortController | undefined;
function sizeLabel(bytes: number) {
  if (bytes < 1024) return bytes + " B";
  const unit = bytes >= 1024 ** 3 ? 3 : bytes >= 1024 ** 2 ? 2 : 1;
  return (
    (bytes / 1024 ** unit).toFixed(1) + " " + ["B", "KiB", "MiB", "GiB"][unit]
  );
}
function timeLabel(value: number) {
  const seconds = Number.isFinite(value) ? Math.max(0, Math.floor(value)) : 0;
  const hours = Math.floor(seconds / 3600);
  const minutes = Math.floor(seconds / 60) % 60;
  return (
    (hours ? hours + ":" + String(minutes).padStart(2, "0") : String(minutes)) +
    ":" +
    String(seconds % 60).padStart(2, "0")
  );
}
const transcodeLabels: Record<string, string> = {
  queued: "转码排队中",
  reserved: "待提交",
  processing: "正在转码",
  ready: "兼容版本就绪",
  failed: "转码失败",
};
const state = ref<State>({
    asset: "",
    position: 0,
    rate: 1,
    playing: false,
    version: 0,
    at: 0,
  }),
  chats = ref<{ id: number; line: number; text: string }[]>([]);
let socket: WebSocket | undefined,
  retry = 0,
  renew = 0,
  tick = 0,
  offset = 0,
  loaded = "",
  seq = 0,
  request = 0;
function fail(e: unknown) {
  msg.value = e instanceof Error ? e.message : String(e);
}
async function join(create: boolean) {
  try {
    configureEndpoint(serverUrl.value);
    const m = await api<Member>(create ? "rooms" : "join", null, {
      name: name.value.trim(),
      invite: invite.value.trim(),
    });
    sessionStorage.setItem("member", JSON.stringify(m));
    enter(m);
  } catch (e) {
    fail(e);
  }
}
function enter(m: Member) {
  member.value = m;
  msg.value = "";
  connect();
  refresh().catch(fail);
}
async function refresh() {
  const current = member.value;
  if (!current) return;
  const list = await api<typeof assets.value | null>("assets", current);
  if (member.value === current) assets.value = list ?? [];
}
function connect() {
  if (!member.value) return;
  const ws = new WebSocket(endpoint.replace(/^http/, "ws") + "/api/v1/live");
  socket = ws;
  let synced = false;
  let bestRtt = Infinity;
  let pingAt = 0;
  let samples = 0;
  let clockTimer = 0;
  function ping() {
    if (socket !== ws || ws.readyState !== WebSocket.OPEN) return;
    pingAt = performance.now();
    ws.send(JSON.stringify({ type: "ping" }));
  }
  ws.onopen = () => {
    ws.send(JSON.stringify({ token: member.value?.token }));
  };
  ws.onmessage = async (event) => {
    if (socket !== ws) return;
    const m = JSON.parse(event.data);
    if (m.type === "pong" && pingAt) {
      const received = performance.now();
      const rtt = received - pingAt;
      if (rtt < bestRtt) {
        bestRtt = rtt;
        offset = m.now - (performance.timeOrigin + pingAt + rtt / 2);
      }
      pingAt = 0;
      samples++;
      if (samples % 3 === 0) {
        clockTimer = window.setTimeout(() => {
          bestRtt = Infinity;
          ping();
        }, 60000);
      } else {
        clockTimer = window.setTimeout(ping, 250);
      }
    }
    if (m.type === "state") {
      connected.value = true;
      if (!synced) {
        offset = m.now - (performance.timeOrigin + performance.now());
        synced = true;
        ping();
      }
      if (
        m.state.asset &&
        m.state.asset !== state.value.asset &&
        !assets.value.some((a) => a.id === m.state.asset)
      )
        refresh().catch(fail);
      state.value = m.state;
      await align().catch(fail);
    }
    if (m.type === "chat") {
      const id = ++seq;
      chats.value.push({ id, line: id % 5, text: m.name + " · " + m.text });
      setTimeout(
        () => (chats.value = chats.value.filter((v) => v.id !== id)),
        7500,
      );
    }
  };
  ws.onclose = () => {
    clearTimeout(clockTimer);
    if (socket !== ws) return;
    connected.value = false;
    retry = window.setTimeout(connect, 1800);
  };
}
function target() {
  const s = state.value;
  return (
    s.position +
    (s.playing
      ? (Math.max(
          0,
          performance.timeOrigin + performance.now() + offset - s.at,
        ) /
          1000) *
        s.rate
      : 0)
  );
}
async function source(id: string) {
  const nonce = ++request;
  const result = await api<{ url: string; ttl: number }>(
    "assets/" + id + "/play",
    member.value,
  );
  if (nonce !== request || id !== state.value.asset) return;
  const v = video.value;
  if (!v) return;
  loaded = id;
  v.src = result.url;
  clearTimeout(renew);
  renew = window.setTimeout(
    () => source(id).catch(fail),
    (result.ttl - 60) * 1000,
  );
}
async function align() {
  const v = video.value,
    s = state.value;
  if (!v || !s.asset) return;
  if (loaded !== s.asset) {
    await source(s.asset);
    return;
  }
  if (v.readyState < 1) return;
  const t = Math.min(
    target(),
    Number.isFinite(v.duration) ? v.duration : Infinity,
  );
  if (Math.abs(v.currentTime - t) > (s.playing ? 0.45 : 0.08))
    v.currentTime = t;
  v.playbackRate = s.rate;
  if (s.playing && v.paused && !v.ended) {
    try {
      await v.play();
    } catch {
      status.value = "点按播放器启用声音与播放";
    }
  } else if (!s.playing && !v.paused) v.pause();
}
function control(p: Partial<State>) {
  if (!connected.value) return;
  socket?.send(
    JSON.stringify({
      type: "control",
      state: { ...state.value, position: target(), ...p },
    }),
  );
}
function toggle() {
  if (state.value.playing && video.value?.paused) {
    video.value.play().catch(fail);
    return;
  }
  control({
    playing: !state.value.playing,
    ...(video.value?.ended ? { position: 0 } : {}),
  });
}
async function select(e: Event) {
  const input = e.target as HTMLInputElement,
    file = input.files?.[0];
  if (!file || !member.value || busy.value) return;
  const controller = new AbortController();
  busy.value = true;
  uploadController = controller;
  transfer.value = {
    name: file.name,
    size: file.size,
    sent: 0,
    phase: "准备上传",
  };
  const current = transfer.value;
  try {
    if (!file.size) throw Error("文件为空，请选择一个视频文件");
    const id = await upload(
      file,
      member.value,
      (phase, bytes) => {
        current.phase = phase;
        current.sent = bytes;
      },
      controller.signal,
    );
    if (controller.signal.aborted) return;
    current.sent = file.size;
    current.phase = "上传完成";
    await refresh().catch(fail);
    if (controller.signal.aborted) return;
    control({ asset: id, position: 0, playing: false });
  } catch (e) {
    if (!controller.signal.aborted) {
      current.phase = "上传失败";
      current.error = e instanceof Error ? e.message : String(e);
    }
  } finally {
    if (uploadController === controller) {
      uploadController = undefined;
      busy.value = false;
    }
    input.value = "";
  }
}
async function transcode(id: string) {
  try {
    await api("assets/" + id + "/transcode", member.value, {});
    await refresh();
    status.value = "已请求兼容播放版本";
  } catch (e) {
    fail(e);
  }
}
function chat() {
  if (text.value.trim() && connected.value) {
    socket?.send(JSON.stringify({ type: "chat", text: text.value.trim() }));
    text.value = "";
  }
}
function leave() {
  uploadController?.abort();
  uploadController = undefined;
  busy.value = false;
  transfer.value = null;
  assets.value = [];
  status.value = "";
  duration.value = position.value = 0;
  clearTimeout(retry);
  clearTimeout(renew);
  const old = socket;
  socket = undefined;
  old?.close();
  sessionStorage.removeItem("member");
  member.value = null;
  connected.value = false;
  loaded = "";
  request++;
  state.value.asset = "";
  chats.value = [];
}
async function fullscreen() {
  if (document.fullscreenElement) await document.exitFullscreen();
  else if (stage.value?.requestFullscreen)
    await stage.value.requestFullscreen();
  else wide.value = !wide.value;
}
async function copy() {
  try {
    await navigator.clipboard.writeText(member.value!.invite);
    status.value = "房间码已复制";
  } catch {
    status.value = member.value!.invite;
  }
}
onMounted(() => {
  const raw = sessionStorage.getItem("member");
  if (raw) {
    const m = JSON.parse(raw);
    if (!invite.value || invite.value === m.invite) enter(m);
  }
  tick = window.setInterval(() => {
    if (
      connected.value &&
      video.value &&
      loaded === state.value.asset &&
      video.value.readyState >= 2
    )
      align().catch(fail);
  }, 3000);
});
onUnmounted(() => {
  clearInterval(tick);
  leave();
});
</script>
<template>
  <header class="mast">
    <div class="bar shell">
      <div class="brand">
        <span class="brand-mark" aria-hidden="true"></span><strong>月映</strong>
      </div>
      <span class="mast-label">共同观影</span>
      <button v-if="member" class="exit" @click="leave">
        <LogOut />离开房间
      </button>
    </div>
  </header>

  <section v-if="!member" class="lobby shell">
    <div class="poster">
      <div class="lunar" aria-hidden="true"></div>
      <div class="poster-caption">
        <h1>一起看部电影<span>。</span></h1>
        <p>与朋友共享画面、进度和实时弹幕。</p>
      </div>
    </div>
    <form class="entry" @submit.prevent="join(!invite.trim())">
      <h2>{{ invite.trim() ? "加入房间" : "创建房间" }}</h2>
      <label
        >你的昵称<input
          v-model="name"
          required
          maxlength="40"
          placeholder="朋友怎么称呼你"
      /></label>
      <label
        >房间码<input v-model="invite" placeholder="新建房间时留空"
      /></label>
      <details v-if="configurable">
        <summary>连接设置</summary>
        <label
          >月映服务地址<input v-model="serverUrl" type="url" required
        /></label>
      </details>
      <button class="primary">
        {{ invite.trim() ? "加入房间" : "创建房间" }}<ArrowRight />
      </button>
    </form>
  </section>

  <article v-else class="room shell">
    <div class="room-head">
      <h1>观影房间</h1>
      <span class="connection" :class="{ online: connected }"
        ><i></i>{{ connected ? "已连接" : "连接中…" }}</span
      >
      <button class="room-code" title="复制房间码" @click="copy">
        房间码 <b>{{ member.invite }}</b
        ><Copy />
      </button>
    </div>
    <div class="layout">
      <section class="screen-column" aria-label="放映区">
        <div ref="stage" class="player" :class="{ wide }">
          <div class="stage">
            <video
              ref="video"
              playsinline
              @click="toggle"
              @playing="align().catch(fail)"
              @loadedmetadata="
                duration = video?.duration || 0;
                align().catch(fail);
              "
              @timeupdate="position = video?.currentTime || 0"
              @ended="control({ playing: false, position: duration })"
              @error="msg = '无法播放此片源，请检查格式或网络'"
            ></video>
            <div v-if="!state.asset" class="empty-stage">
              <div class="lunar" aria-hidden="true"></div>
              <div>
                <Film />
                <h2>等待放映</h2>
                <p>上传视频，或从片源列表中选择。</p>
              </div>
            </div>
            <div
              v-for="c in chats"
              :key="c.id"
              class="bullet"
              :style="{ top: 6 + c.line * 13 + '%' }"
            >
              {{ c.text }}
            </div>
          </div>
          <div class="controls">
            <input
              aria-label="播放进度"
              type="range"
              min="0"
              :max="duration || 1"
              step=".1"
              :value="position"
              :disabled="!duration"
              :style="{
                '--played': (duration ? (position / duration) * 100 : 0) + '%',
              }"
              @change="
                control({
                  position: Number(($event.target as HTMLInputElement).value),
                })
              "
            />
            <div class="transport">
              <button
                class="play"
                :aria-label="state.playing ? '暂停' : '播放'"
                :disabled="!state.asset"
                @click="toggle"
              >
                <Pause v-if="state.playing" /><Play v-else />
              </button>
              <button
                aria-label="−10秒"
                title="后退 10 秒"
                :disabled="!state.asset"
                @click="control({ position: Math.max(0, target() - 10) })"
              >
                <RotateCcw /><span>10</span>
              </button>
              <button
                aria-label="+10秒"
                title="快进 10 秒"
                :disabled="!state.asset"
                @click="
                  control({ position: Math.min(duration, target() + 10) })
                "
              >
                <RotateCw /><span>10</span>
              </button>
              <span class="time"
                >{{ timeLabel(position) }} <i>/</i>
                {{ timeLabel(duration) }}</span
              >
              <select
                aria-label="倍速"
                :value="state.rate"
                @change="
                  control({
                    rate: Number(($event.target as HTMLSelectElement).value),
                  })
                "
              >
                <option v-for="n in [0.5, 1, 1.25, 1.5, 2]" :key="n" :value="n">
                  {{ n }}×
                </option>
              </select>
              <button
                :aria-label="wide ? '退出影院' : '全屏'"
                title="切换全屏"
                @click="wide ? (wide = false) : fullscreen().catch(fail)"
              >
                <Maximize />
              </button>
            </div>
          </div>
        </div>
        <form class="chat" @submit.prevent="chat">
          <label for="danmaku"><MessageSquare />弹幕</label
          ><input
            id="danmaku"
            v-model="text"
            maxlength="200"
            placeholder="发一条实时弹幕"
          />
          <button :disabled="!connected || !text.trim()" aria-label="发送">
            发送<Send />
          </button>
        </form>
        <small v-if="status" class="notice" role="status">{{ status }}</small>
      </section>
      <aside class="library">
        <div class="library-head">
          <h2>房间片源</h2>
          <button
            aria-label="刷新列表"
            title="刷新列表"
            @click="refresh().catch(fail)"
          >
            <RefreshCw />
          </button>
        </div>
        <label class="pick" :class="{ busy }"
          ><input
            type="file"
            accept="video/*,.mkv"
            :disabled="busy"
            @change="select"
          />
          <span class="upload-symbol"><UploadIcon /></span
          ><span>{{ busy ? "正在处理上传" : "上传本地视频" }}</span>
        </label>
        <div
          v-if="transfer"
          class="transfer"
          :class="{ failed: transfer.phase === '上传失败' }"
        >
          <div class="progress-head">
            <strong role="status">{{ transfer.phase }}</strong
            ><b>{{ percent }}%</b>
          </div>
          <progress
            aria-label="视频上传进度"
            :value="transfer.sent"
            :max="transfer.size || 1"
          ></progress>
          <div class="transfer-name" :title="transfer.name">
            {{ transfer.name }}
          </div>
          <small
            >{{ sizeLabel(transfer.sent) }} /
            {{ sizeLabel(transfer.size) }}</small
          >
          <p v-if="transfer.error" role="alert">{{ transfer.error }}</p>
        </div>
        <div class="list-heading">
          <h3>播放列表</h3>
          <span>{{ assets.length }} 部</span>
        </div>
        <p v-if="!assets.length" class="empty-list"><FolderOpen />暂无片源</p>
        <div
          v-for="a in assets"
          :key="a.id"
          class="asset"
          :class="{ selected: state.asset === a.id }"
        >
          <button
            class="asset-select"
            :aria-pressed="state.asset === a.id"
            :aria-label="a.name"
            @click="control({ asset: a.id, position: 0, playing: false })"
          >
            <span class="asset-icon"><Film /></span
            ><span class="asset-info"
              ><strong>{{ a.name }}</strong
              ><small>{{
                state.asset === a.id ? "当前片源" : "选择播放"
              }}</small></span
            ><Check v-if="state.asset === a.id" class="check" />
          </button>
          <button
            v-if="!a.transcode"
            class="transcode"
            @click="transcode(a.id)"
          >
            申请转码</button
          ><small v-else
            >{{ transcodeLabels[a.transcode] }} {{ a.detail }}</small
          >
        </div>
      </aside>
    </div>
  </article>
  <div v-if="msg" class="error" role="alert">
    {{ msg }} <button @click="msg = ''">关闭</button>
  </div>
</template>
