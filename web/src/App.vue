<script setup lang="ts">
import { ref, onMounted, onUnmounted } from "vue";
import { api, endpoint, configureEndpoint, upload, type Member } from "./api";
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
  busy = ref(false),
  connected = ref(false),
  position = ref(0),
  duration = ref(0),
  wide = ref(false);
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
  assets.value = await api("assets", member.value);
}
function connect() {
  if (!member.value) return;
  const ws = new WebSocket(endpoint.replace(/^http/, "ws") + "/api/v1/live");
  socket = ws;
  ws.onopen = () => {
    ws.send(JSON.stringify({ token: member.value?.token }));
  };
  ws.onmessage = async (event) => {
    const m = JSON.parse(event.data);
    if (m.type === "state") {
      connected.value = true;
      offset = m.now - Date.now();
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
    if (socket !== ws) return;
    connected.value = false;
    retry = window.setTimeout(connect, 1800);
  };
}
function target() {
  const s = state.value;
  return (
    s.position +
    (s.playing ? (Math.max(0, Date.now() + offset - s.at) / 1000) * s.rate : 0)
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
  if (Math.abs(v.currentTime - t) > 0.45) v.currentTime = t;
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
  if (!file || !member.value) return;
  busy.value = true;
  try {
    const id = await upload(
      file,
      member.value,
      (n) => (status.value = "正在上传 " + n + "%"),
    );
    await refresh();
    control({ asset: id, position: 0, playing: false });
    status.value = "上传完成";
  } catch (e) {
    fail(e);
  } finally {
    busy.value = false;
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
  <header>
    <strong>月映</strong><span>共同观影</span
    ><button v-if="member" @click="leave">离开房间</button>
  </header>
  <section v-if="!member" class="entry">
    <h1>一起看部电影</h1>
    <form @submit.prevent="join(!!!invite.trim())">
      <label
        >你的昵称<input
          v-model="name"
          required
          maxlength="40"
          placeholder="朋友怎么称呼你" /></label
      ><label
        >房间码<input v-model="invite" placeholder="新建房间时留空"
      /></label>
      <details v-if="configurable">
        <summary>连接设置</summary>
        <label
          >月映服务地址<input v-model="serverUrl" type="url" required
        /></label>
      </details>
      <button class="primary">
        {{ invite.trim() ? "加入房间" : "创建房间" }}
      </button>
    </form>
  </section>
  <article v-else>
    <nav>
      <h1>观影房间</h1>
      <button @click="copy">房间码 {{ member.invite }}</button
      ><small>{{ connected ? "已连接" : "连接中…" }}</small>
    </nav>
    <div class="layout">
      <div>
        <div class="stage" :class="{ wide }" ref="stage">
          <video
            ref="video"
            playsinline
            @click="toggle"
            @loadedmetadata="
              duration = video?.duration || 0;
              align().catch(fail);
            "
            @timeupdate="position = video?.currentTime || 0"
            @ended="control({ playing: false, position: duration })"
            @error="msg = '无法播放此片源，请检查格式或网络'"
          ></video>
          <p v-if="!state.asset">选择片源开始观影</p>
          <div
            v-for="c in chats"
            :key="c.id"
            class="bullet"
            :style="{ top: 6 + c.line * 13 + '%' }"
          >
            {{ c.text }}
          </div>
          <button v-if="wide" @click="wide = false">退出影院</button>
        </div>
        <div class="controls">
          <button :disabled="!state.asset" @click="toggle">
            {{ state.playing ? "暂停" : "播放" }}</button
          ><button @click="control({ position: Math.max(0, target() - 10) })">
            −10秒</button
          ><input
            aria-label="播放进度"
            type="range"
            min="0"
            :max="duration || 1"
            step=".1"
            :value="position"
            @change="
              control({
                position: Number(($event.target as HTMLInputElement).value),
              })
            "
          /><button
            @click="control({ position: Math.min(duration, target() + 10) })"
          >
            +10秒</button
          ><select
            aria-label="倍速"
            :value="state.rate"
            @change="
              control({
                rate: Number(($event.target as HTMLSelectElement).value),
              })
            "
          >
            <option v-for="n in [0.5, 1, 1.25, 1.5, 2]" :value="n">
              {{ n }}×
            </option></select
          ><button @click="fullscreen().catch(fail)">全屏</button>
        </div>
        <form class="chat" @submit.prevent="chat">
          <input
            v-model="text"
            maxlength="200"
            placeholder="发一条实时弹幕"
          /><button :disabled="!connected">发送</button>
        </form>
      </div>
      <aside>
        <h2>房间片源</h2>
        <label class="pick"
          >{{ busy ? "上传中…" : "上传本地视频"
          }}<input
            type="file"
            :disabled="busy"
            accept="video/*,.mkv"
            @change="select" /></label
        ><button @click="refresh().catch(fail)">刷新列表</button>
        <div v-for="a in assets" :key="a.id">
          <button
            class="item"
            @click="control({ asset: a.id, position: 0, playing: false })"
          >
            {{ a.name }}</button
          ><button v-if="!a.transcode" @click="transcode(a.id)">申请转码</button
          ><small v-else
            >{{ transcodeLabels[a.transcode] }} {{ a.detail }}</small
          >
        </div>
      </aside>
    </div>
    <small>{{ status }}</small>
  </article>
  <div v-if="msg" class="error" role="alert">
    {{ msg }} <button @click="msg = ''">关闭</button>
  </div>
</template>
