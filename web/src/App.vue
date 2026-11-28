<script setup lang="ts">
import { computed, ref, watch, onMounted, onUnmounted } from "vue";
import {
  Link as LinkIcon,
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
import { api, endpoint, constrainedNetwork, upload, type Member, type UploadPhase } from "./api";
import { useRoomVoice } from "./voice";
import AccountMenu from "./AccountMenu.vue";
import { session, accountApi, accountJoin } from "./account";
import { forget } from "./rooms";
import Lobby from "./Lobby.vue";
import RoomManagement from "./RoomManagement.vue";
import {
  roomInfo,
  remember,
  type RoomInfo,
  type Access,
  type Ability,
} from "./rooms";
type State = {
  asset: string;
  position: number;
  rate: number;
  playing: boolean;
  version: number;
  at: number;
};
const member = ref<Member | null>(null);
const info = ref<RoomInfo>();
const manage = ref(false),
  focusMember = ref("");
const can = (ability: Ability) =>
  !!info.value &&
  (info.value.self.role !== "member" || !!info.value.self.permissions[ability]);
const voiceAvailable = ref(false);
const { mic, speaker, needsGesture, hint, pending, toggleMic, toggleSpeaker, resumeAudio, receive, disconnectVoice, resetVoice } =
  useRoomVoice((frame) => {
    if (socket?.readyState === WebSocket.OPEN && socket.bufferedAmount < 8192)
      socket.send(JSON.stringify({ type: "voice", text: frame }));
  }, computed(() => voiceAvailable.value && can("chat")));
const copyNotice = ref(""),
  manualCopy = ref(""),
  copyFailure = ref(false);
let copyTimer = 0,
  copyRequest = 0;
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
  bytesPerSecond?: number;
  phase: UploadPhase | "已暂停" | "上传完成" | "上传失败";
  error?: string;
};
const transfer = ref<Transfer | null>(null);
const busy = ref(false);
const percent = computed(() => {
  const t = transfer.value;
  return t?.size ? Math.floor((100 * t.sent) / t.size) : 0;
});
let pendingFile: File | undefined;
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
  expiresAt = 0,
  tick = 0,
  offset = 0,
  loaded = "",
  seq = 0,
  request = 0;
type Participant = Access & {
  name: string;
  rtt: number | null;
  playback?: {
    version?: number;
    at?: number;
    rate?: number;
    asset: string;
    position: number;
    phase: "playing" | "paused" | "buffering";
  };
};
const members = ref<Participant[]>([]);
const ahead = ref(0),
  waiting = ref(false),
  blocked = ref(false),
  showPlay = ref(false);
let feedbackTimer = 0,
  commandTimer = 0;
const displayClock = ref(0);
const pendingNames = ref<string[]>([]);
let readyVersion = -1;
const bufferedTrack = ref("");
function memberProgress(person: Participant) {
  const own = person.id === info.value?.self.id;
  const sample = own ? samplePlayback() : person.playback;
  if (!sample || sample.asset !== state.value.asset) return "等待进度";
  if (
    !own &&
    sample.version !== undefined &&
    sample.version !== state.value.version
  )
    return "等待新进度";
  const age = sample.at
    ? Math.max(0, (displayClock.value - sample.at) / 1000)
    : 0;
  if (!own && age > 12) return "进度待更新";
  const phase =
    sample.phase === "playing"
      ? "播放中"
      : sample.phase === "buffering"
        ? "缓冲中"
        : "暂停";
  const estimate =
    sample.phase === "playing" && sample.at
      ? Math.min(age, 3) * (sample.rate || 1)
      : 0;
  return (
    timeLabel(
      own
        ? position.value
        : Math.min(duration.value || Infinity, sample.position + estimate),
    ) +
    " · " +
    phase +
    (own ? "" : estimate ? " · 估算" : " · 上报")
  );
}
const pendingToggle = ref<boolean | null>(null);
const slowCommand = ref(false);
const togglePauses = computed(
  () =>
    !blocked.value && (state.value.playing || pendingNames.value.length > 0),
);
const canToggle = computed(
  () =>
    connected.value &&
    !!state.value.asset &&
    pendingToggle.value === null &&
    ((state.value.playing && blocked.value) ||
      can(togglePauses.value ? "pause" : "play")),
);
const toggleLabel = computed(() => {
  if (!connected.value) return "正在重连，暂时无法控制播放";
  if (slowCommand.value) return pendingToggle.value ? "播放请求已发送，等待服务器确认" : "暂停请求已发送，等待服务器确认";
  if (pendingToggle.value !== null)
    return pendingToggle.value ? "正在请求播放…" : "正在请求暂停…";
  if (!canToggle.value)
    return togglePauses.value ? "暂停需房主授权" : "播放需房主授权";
  if (pendingNames.value.length) return "取消等待并暂停";
  return togglePauses.value ? "暂停播放" : "继续播放";
});
const feedback = computed(() => {
  if (!connected.value || pendingToggle.value !== null)
    return toggleLabel.value;
  if (pendingNames.value.length)
    return "等待缓冲 · " + pendingNames.value.join("、");
  if (!state.value.playing)
    return canToggle.value ? "已暂停 · 继续播放" : toggleLabel.value;
  if (blocked.value) return "点按播放";
  if (waiting.value) return "缓冲中";
  return showPlay.value || controlsVisible.value ? toggleLabel.value : "";
});
function resetCommand() {
  clearTimeout(commandTimer);
  pendingToggle.value = null;
  slowCommand.value = false;
}
let opening = "",
  needsSeek = false,
  bufferingSince = 0,
  lastJump = -Infinity;
let canJumpOutsideBuffer = true;
let playAttempt: Promise<void> | undefined;
function bufferAhead(v: HTMLVideoElement, at: number) {
  for (let i = 0; i < v.buffered.length; i++) {
    if (v.buffered.start(i) <= at + 0.05 && v.buffered.end(i) > at)
      return v.buffered.end(i) - at;
  }
  return 0;
}
let gateStarted = 0;
let lastStall = -Infinity;
function onWaiting() {
  const v = video.value;
  if (v && state.value.playing && !v.seeking && v.currentTime > 0 && bufferAhead(v, v.currentTime) < 1)
    lastStall = performance.now();
  beginBuffering();
  reportPlayback();
}
function needsExtraBuffer() {
  return constrainedNetwork() || performance.now() - lastStall < 60000;
}
function bufferGoal(v: HTMLVideoElement) {
  return Math.min((needsExtraBuffer() ? 15 : 6) * state.value.rate, Math.max(0, v.duration - v.currentTime));
}
function measureBuffer() {
  const v = video.value;
  if (!v) return;
  ahead.value = bufferAhead(v, v.currentTime);
  const stops = ["var(--played-color,var(--amber)) 0 var(--played)"];
  if (Number.isFinite(v.duration) && v.duration > 0) {
    for (let i = 0; i < v.buffered.length; i++) {
      const start = (100 * v.buffered.start(i)) / v.duration,
        end = (100 * v.buffered.end(i)) / v.duration;
      stops.push(
        "transparent " + start + "%",
        "#a6b9ab " + start + "% " + end + "%",
        "transparent " + end + "%",
      );
    }
  }
  bufferedTrack.value =
    "linear-gradient(to right," +
    stops.join(",") +
    ",transparent 100%),#565b54";
  if (
    pendingNames.value.length &&
    readyVersion !== state.value.version &&
    loaded === state.value.asset &&
    !v.seeking &&
    v.readyState >= 3 &&
    Math.abs(v.currentTime - Math.min(state.value.position, v.duration)) <
      0.25 &&
    (ahead.value + 0.1 >=
      bufferGoal(v) ||
      (performance.now() - gateStarted >= 8000 && v.readyState >= 3 && ahead.value >= 1))
  ) {
    if (socket?.readyState === WebSocket.OPEN) {
      readyVersion = state.value.version;
      socket.send(
        JSON.stringify({ type: "ready", state: { version: readyVersion } }),
      );
    }
  }
}
function beginBuffering() {
  if (!state.value.playing || blocked.value) return;
  if (!waiting.value) bufferingSince = performance.now();
  waiting.value = true;
}
function playbackStarted() {
  if (!state.value.playing) {
    video.value?.pause();
    return;
  }
  waiting.value = blocked.value = false;
}
function flashPlay() {
  clearTimeout(feedbackTimer);
  showPlay.value = true;
  feedbackTimer = window.setTimeout(() => (showPlay.value = false), 850);
}
function fail(e: unknown) {
  msg.value = e instanceof Error ? e.message : String(e);
}
function showPermissions(id: string) {
  focusMember.value = id;
  manage.value = true;
}
function samplePlayback() {
  const v = video.value;
  if (!v || loaded !== state.value.asset || !loaded || v.readyState < 1)
    return null;
  return {
    version: state.value.version,
    at: Math.round(performance.timeOrigin + performance.now() + offset),
    rate: v.playbackRate,
    asset: loaded,
    position: v.currentTime,
    phase: waiting.value ? "buffering" : v.paused ? "paused" : "playing",
  };
}
let sampleTimer = 0;
function reportPlayback() {
  if (sampleTimer) return;
  sampleTimer = window.setTimeout(() => {
    sampleTimer = 0;
    if (socket?.readyState === WebSocket.OPEN)
      socket.send(
        JSON.stringify({ type: "progress", playback: samplePlayback() }),
      );
  }, 950);
}
async function reloadInfo() {
  const current = member.value;
  if (!current) return;
  const result = await roomInfo(current);
  if (member.value !== current) return;
  info.value = result;
  remember(current, result);
}
watch(session, (current) => {
  if (member.value?.accountId && member.value.accountId !== current?.id)
    leave();
});
async function bindCurrent() {
  const m = member.value;
  if (!m || !session.value) return;
  await accountApi("account/claim", { guest: m.token });
  const joined = await accountJoin(m.invite, m.name);
  forget(m.invite);
  leave();
  enter(joined);
}
function enter(m: Member) {
  manage.value = false;
  member.value = m;
  sessionStorage.setItem("member", JSON.stringify(m));
  const url = new URL(location.href);
  url.searchParams.set("room", m.invite);
  history.replaceState(null, "", url);
  msg.value = "";
  reloadInfo().catch(fail);
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
  const clockSamples: { rtt: number; offset: number }[] = [];
  let probe = 0,
    pingAt = 0,
    clockTimer = 0,
    watchdog = window.setTimeout(() => ws.close(), 25000);
  function ping() {
    if (socket !== ws || ws.readyState !== WebSocket.OPEN) return;
    pingAt = performance.now();
    ws.send(JSON.stringify({ type: "ping", probe: ++probe }));
    watchdog = window.setTimeout(() => ws.close(), 25000);
  }
  ws.onopen = () => {
    ws.send(
      JSON.stringify({
        token: member.value?.token,
        voice: typeof AudioContext !== "undefined",
        presence: true,
        bufferSync: true,
      }),
    );
  };
  ws.onmessage = async (event) => {
    if (socket !== ws) return;
    const m = JSON.parse(event.data);
    if (m.type === "voiceAvailable") voiceAvailable.value = true;
    if (m.type === "voice") { receive(m.from, m.text); return; }
    if (
      m.type === "pong" &&
      pingAt &&
      (m.probe === probe || m.probe === undefined)
    ) {
      clearTimeout(watchdog);
      const rtt = performance.now() - pingAt;
      if (m.probe === probe)
        ws.send(
          JSON.stringify({
            type: "latency",
            probe,
            rtt: Math.round(rtt),
            playback: samplePlayback(),
          }),
        );
      if (rtt < 2000) {
        clockSamples.push({
          rtt,
          offset: m.now - performance.timeOrigin - pingAt - rtt / 2,
        });
        if (clockSamples.length > 8) clockSamples.shift();
        offset = clockSamples.reduce((a, b) => (a.rtt <= b.rtt ? a : b)).offset;
      }
      pingAt = 0;
      clockTimer = window.setTimeout(ping, probe < 3 ? 250 : 4000);
    }
    if (m.type === "error") {
      resetCommand();
      fail(m.message);
    }
    if (m.type === "access" && info.value) {
      info.value.self = m.self;
      if (!can("upload")) pauseUpload();
    }
    if (m.type === "settings") reloadInfo().catch(fail);
    if (m.type === "presence") members.value = m.members;
    if (m.type === "bufferWait" && m.version === state.value.version) {
      if (!pendingNames.value.length) gateStarted = performance.now();
      pendingNames.value = m.names;
      measureBuffer();
    }
    if (m.type === "state") {
      if (m.state.version !== state.value.version || !synced) {
        pendingNames.value = [];
        readyVersion = -1;
      }
      connected.value = true;
      const first = !synced;
      if (first) {
        clearTimeout(watchdog);
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
      resetCommand();
      const next: State = m.state;
      const prev = state.value;
      if (prev.asset !== next.asset) clearQuality();
      const priorPosition =
        prev.position +
        (prev.playing
          ? (Math.max(0, next.at - prev.at) * prev.rate) / 1000
          : 0);
      if (
        first ||
        prev.asset !== next.asset ||
        !next.playing ||
        Math.abs(next.position - priorPosition) > 0.75
      )
        needsSeek = true;
      state.value = next;
      if (next.playing && !prev.playing) {
        beginBuffering();
        flashPlay();
      }
      if (!next.playing) waiting.value = blocked.value = showPlay.value = false;
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
    clearTimeout(watchdog);
    if (socket !== ws) return;
    voiceAvailable.value = false;
    disconnectVoice();
    connected.value = false;
    pendingNames.value = [];
    members.value = [];
    resetCommand();
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
type Quality = "default" | "low";
const quality = ref<Quality>("default");
const qualityNote = ref("");
let qualityTimer = 0, qualityRequest = 0;
function clearQuality() {
  clearTimeout(qualityTimer);
  qualityRequest++;
  quality.value = "default";
  qualityNote.value = "";
}
async function setQuality(value: Quality) {
  clearQuality();
  const generation = qualityRequest;
  const id = state.value.asset;
  quality.value = value;
  if (!id) return;
  if (value === "default") {
    await source(id).catch(fail);
    return;
  }
  let firstCheck = true;
  async function check() {
    try {
      const result = await api<{status:string}>("assets/" + id + (firstCheck ? "/low?retry=1" : "/low"), member.value, {});
      firstCheck = false;
      if (generation !== qualityRequest || state.value.asset !== id) return;
      if (result.status === "ready") {
        await source(id, "low");
        if (generation === qualityRequest) qualityNote.value = "";
      } else if (result.status === "queued" || result.status === "processing") {
        qualityNote.value = "正在准备省流版，完成后本机自动切换";
        qualityTimer = window.setTimeout(check, 5000);
      } else {
        clearQuality();
        qualityNote.value = result.status === "already_small" ? "当前版本已更省流，继续播放" : "省流版生成失败，继续播放当前版本";
      }
    } catch (err) {
      if (generation !== qualityRequest) return;
      clearQuality();
      fail(err);
    }
  }
  qualityNote.value = "正在检查省流版";
  await check();
}
let activeQuality: Quality = "default";
async function source(id: string, mode: Quality = "default", force = false) {
  const key = id + ":" + mode;
  if (opening === key || (!force && loaded === id && activeQuality === mode && !opening)) return;
  const nonce = ++request;
  opening = key;
  try {
    const result = await api<{ url: string; ttl: number }>(
      "assets/" + id + "/play" + (mode === "low" ? "?quality=low" : ""),
      member.value,
    );
    if (nonce !== request || id !== state.value.asset || !video.value) return;
    loaded = id;
    activeQuality = mode;
    expiresAt = Date.now() + result.ttl * 1000;
    needsSeek = true;
    ahead.value = position.value = duration.value = 0;
    blocked.value = false;
    playAttempt = undefined;
    video.value.src = result.url;
    video.value.load();
    beginBuffering();
  } finally {
    if (nonce === request) opening = "";
  }
}
function mediaError() {
  if (
    state.value.asset &&
    loaded === state.value.asset &&
    Date.now() >= expiresAt - 60000
  ) {
    source(loaded, activeQuality, true).catch(fail);
  } else if (activeQuality === "low" && state.value.asset === loaded) {
    void setQuality("default");
    qualityNote.value = "省流版加载失败，正在恢复默认画质";
  } else {
    msg.value = "无法播放此片源，请检查格式或网络";
  }
}
function startPlayback(v: HTMLVideoElement) {
  if (playAttempt || blocked.value) return;
  const attempt = v.play();
  playAttempt = attempt;
  attempt
    .catch((e: DOMException) => {
      if (playAttempt !== attempt || !state.value.playing) return;
      if (e.name === "NotAllowedError") {
        blocked.value = true;
        waiting.value = false;
      } else if (e.name !== "AbortError") fail(e);
    })
    .finally(() => {
      if (playAttempt === attempt) playAttempt = undefined;
    });
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
  const now = performance.now();
  const t = Math.min(
    target(),
    Number.isFinite(v.duration) ? v.duration : Infinity,
  );
  const drift = t - v.currentTime;
  if (!s.playing && !v.paused) v.pause();
  if (needsSeek) {
    needsSeek = false;
    canJumpOutsideBuffer = true;
    v.playbackRate = s.rate;
    if (Math.abs(drift) > 0.08) {
      v.currentTime = t;
      lastJump = now;
      beginBuffering();
    }
  }
  measureBuffer();
  if (!s.playing) {
    v.playbackRate = s.rate;
    return;
  }
  if (v.seeking || blocked.value || v.ended) return;
  if (Math.abs(drift) < 1) canJumpOutsideBuffer = true;
  if (waiting.value || v.paused || v.readyState < 3) {
    beginBuffering();
    // A browser can cap preloading while paused. Allow native playback to take over.
    const goal = bufferGoal(v);
    if (
      ahead.value + 0.1 < goal &&
      now - bufferingSince < (needsExtraBuffer() ? 10000 : 6000)
    ) {
      if (!v.paused) v.pause();
      return;
    }
    if (v.paused) startPlayback(v);
    return;
  }
  // Never chase an advancing clock with a series of unbuffered range requests.
  if (
    Math.abs(drift) > 2 &&
    now - lastJump > 5000 &&
    (bufferAhead(v, t) >= Math.min(1, v.duration - t) ||
      (canJumpOutsideBuffer && Math.abs(drift) > 10))
  ) {
    canJumpOutsideBuffer = false;
    lastJump = now;
    v.currentTime = t;
    beginBuffering();
    return;
  }
  const adjustment =
    Math.abs(drift) < 0.12 ? 0 : Math.max(-0.08, Math.min(0.08, drift * 0.12));
  const rate = s.rate * (1 + adjustment);
  if (Math.abs(v.playbackRate - rate) > 0.005) v.playbackRate = rate;
}
function control(p: Partial<State>) {
  if (!connected.value) return;
  const action =
    p.asset !== undefined
      ? "select"
      : p.playing !== undefined
        ? p.playing
          ? "play"
          : "pause"
        : p.rate !== undefined
          ? "rate"
          : "seek";
  const ability =
    action === "seek"
      ? (p.position ?? 0) >= target()
        ? "forward"
        : "back"
      : action;
  if (!can(ability)) {
    resetCommand();
    fail("房主尚未授权此操作");
    return;
  }
  socket?.send(
    JSON.stringify({
      type: "command",
      action,
      replay: action === "play" && !!video.value?.ended,
      state: { version: state.value.version, ...p },
    }),
  );
}
const controlsVisible = ref(false);
let controlsTimer = 0;
function revealControls() {
  clearTimeout(controlsTimer);
  controlsVisible.value = !!state.value.asset || wide.value;
  if (controlsVisible.value)
    controlsTimer = window.setTimeout(
      () => (controlsVisible.value = false),
      3000,
    );
}
watch(() => state.value.asset, revealControls);
function clickPicture(event: MouseEvent) {
  const firstTap =
    !controlsVisible.value &&
    ("pointerType" in event
      ? event.pointerType === "touch"
      : matchMedia("(pointer: coarse)").matches);
  revealControls();
  if (!firstTap) toggle();
}
function toggle() {
  if (!canToggle.value) return;
  if (state.value.playing && blocked.value && video.value) {
    blocked.value = false;
    startPlayback(video.value);
    return;
  }
  if (pendingNames.value.length) {
    control({ playing: false });
    return;
  }
  pendingToggle.value = !state.value.playing;
  commandTimer = window.setTimeout(() => {
    slowCommand.value = true;
    commandTimer = window.setTimeout(() => socket?.close(), 20000);
  }, 10000);
  control({
    playing: !state.value.playing,
    position: video.value?.ended ? 0 : (video.value?.currentTime ?? target()),
  });
}
function select(e: Event) {
  const input = e.target as HTMLInputElement,
    file = input.files?.[0];
  input.value = "";
  if (file) void runUpload(file);
}
function pauseUpload() {
  if (!uploadController || !transfer.value) return;
  transfer.value.phase = "已暂停";
  uploadController.abort();
}
async function runUpload(file = pendingFile) {
  if (!file || !member.value || busy.value || !can("upload")) return;
  pendingFile = file;
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
  const startedAt = performance.now();
  try {
    if (!file.size) throw Error("文件为空，请选择一个视频文件");
    const id = await upload(
      file,
      member.value,
      (phase, bytes, transferred) => {
        if (controller.signal.aborted) return;
        current.phase = phase;
        current.sent = bytes;
        const seconds = (performance.now() - startedAt) / 1000;
        if (transferred && seconds >= 1)
          current.bytesPerSecond = Math.round(transferred / seconds);
      },
      controller.signal,
    );
    if (controller.signal.aborted) return;
    current.sent = file.size;
    pendingFile = undefined;
    current.phase = "上传完成";
    await refresh().catch(fail);
    if (controller.signal.aborted) return;
    if (can("select")) control({ asset: id, position: 0, playing: false });
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
  clearQuality();
  pendingFile = undefined;
  uploadController?.abort();
  uploadController = undefined;
  busy.value = false;
  transfer.value = null;
  assets.value = [];
  status.value = "";
  clearTimeout(copyTimer);
  copyRequest++;
  copyNotice.value = manualCopy.value = "";
  info.value = undefined;
  manage.value = false;
  focusMember.value = "";
  const url = new URL(location.href);
  url.searchParams.delete("room");
  history.replaceState(null, "", url);
  duration.value = position.value = 0;
  clearTimeout(retry);
  clearTimeout(sampleTimer);
  sampleTimer = 0;
  clearTimeout(feedbackTimer);
  resetCommand();
  pendingNames.value = [];
  readyVersion = -1;
  bufferedTrack.value = "";
  members.value = [];
  waiting.value = blocked.value = showPlay.value = false;
  opening = "";
  playAttempt = undefined;
  voiceAvailable.value = false;
  resetVoice();
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
async function copyInvitation(link: boolean) {
  const current = member.value;
  if (!current) return;
  const url = new URL(
    import.meta.env.PROD &&
    (!/^https?:$/.test(location.protocol) ||
      ["localhost", "tauri.localhost"].includes(location.hostname))
      ? "https://rain-ziyu.github.io/yueying/"
      : location.href,
  );
  url.search = "";
  url.hash = "";
  url.searchParams.set("room", current.invite);
  const value = link ? url.href : current.invite;
  const id = ++copyRequest;
  clearTimeout(copyTimer);
  manualCopy.value = "";
  copyNotice.value = "";
  copyFailure.value = false;
  try {
    await navigator.clipboard.writeText(value);
    if (id !== copyRequest) return;
    copyNotice.value = link ? "邀请链接已复制" : "房间码已复制";
    copyTimer = window.setTimeout(() => (copyNotice.value = ""), 3000);
  } catch {
    if (id !== copyRequest) return;
    copyFailure.value = true;
    copyNotice.value = "复制失败，请选中后手动复制";
    manualCopy.value = value;
  }
}
onMounted(() => {
  const raw = sessionStorage.getItem("member");
  if (raw) {
    const m = JSON.parse(raw);
    const code = new URL(location.href).searchParams.get("room");
    if (
      (!m.accountId || session.value?.id === m.accountId) &&
      (!code || code === m.invite)
    )
      enter(m);
  }
  tick = window.setInterval(() => {
    displayClock.value = performance.timeOrigin + performance.now() + offset;
    if (
      connected.value &&
      video.value &&
      loaded === state.value.asset &&
      video.value.readyState >= 1
    )
      align().catch(fail);
  }, 500);
});
onUnmounted(() => {
  clearTimeout(controlsTimer);
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
      <AccountMenu />
      <button v-if="member" class="exit" @click="leave">
        <LogOut />离开房间
      </button>
    </div>
  </header>

  <Lobby v-if="!member" @enter="enter" @error="fail" />
  <article v-else class="room shell">
    <div class="room-head">
      <h1>{{ info?.title || "观影房间" }}</h1>
      <span class="connection" :class="{ online: connected }"
        ><i></i>{{ connected ? "已连接" : "连接中…" }}</span
      >
      <div v-if="voiceAvailable" class="invite-tools" aria-label="房间语音">
        <button class="invite-link" :aria-pressed="mic" :disabled="!can('chat')" @click="toggleMic">
          {{ !can('chat') ? '已禁言' : pending ? '取消开启麦克风' : mic ? '关闭麦克风' : '开启麦克风' }}
        </button>
        <button class="invite-link" :aria-pressed="speaker" @click="toggleSpeaker">
          {{ speaker ? '关闭语音扬声器' : '开启语音扬声器' }}
        </button>
        <button v-if="needsGesture && (speaker || mic)" class="invite-link" @click="resumeAudio">启用设备音频</button>
        <small v-if="hint" role="status">{{ hint }}</small>
      </div>
      <div class="invite-tools">
        <button
          v-if="session && !member.accountId"
          class="invite-link"
          @click="bindCurrent().catch(fail)"
        >
          绑定当前房间身份
        </button>
        <button
          class="room-code"
          title="复制房间码"
          aria-label="复制房间码"
          @click="copyInvitation(false)"
        >
          房间码 <b>{{ member.invite }}</b
          ><Copy />
        </button>
        <button class="invite-link" @click="copyInvitation(true)">
          <LinkIcon />复制链接
        </button>
        <button
          v-if="info?.self.role === 'owner'"
          class="invite-link"
          aria-haspopup="dialog"
          @click="showPermissions('')"
        >
          房间管理
        </button>
        <div
          v-if="copyNotice"
          class="copy-feedback"
          :class="{ failed: copyFailure }"
          role="status"
        >
          {{ copyNotice
          }}<input
            v-if="manualCopy"
            readonly
            :value="manualCopy"
            aria-label="手动复制邀请"
            @focus="($event.target as HTMLInputElement).select()"
          />
        </div>
      </div>
    </div>
    <RoomManagement
      v-if="manage && info?.self.role === 'owner'"
      :member="member"
      :info="info"
      :focus="focusMember"
      :online="members.map((m) => m.id)"
      @update="reloadInfo().catch(fail)"
      @close="manage = false"
    />
    <div class="layout">
      <section class="screen-column" aria-label="放映区">
        <div
          ref="stage"
          class="player"
          :class="{ wide }"
          @pointermove="$event.pointerType !== 'touch' && revealControls()"
          @focusin="revealControls"
          @keydown="revealControls"
        >
          <div class="stage">
            <video
              ref="video"
              playsinline
              preload="auto"
              @click="clickPicture"
              @playing="
                playbackStarted();
                reportPlayback();
              "
              @pause="reportPlayback"
              @seeked="reportPlayback"
              @waiting="onWaiting"
              @progress="measureBuffer"
              @canplay="align().catch(fail)"
              @loadedmetadata="
                duration = video?.duration || 0;
                align().catch(fail);
              "
              @timeupdate="
                position = video?.currentTime || 0;
                reportPlayback();
                measureBuffer();
              "
              @ended="
                can('pause') && control({ playing: false, position: duration })
              "
              @error="mediaError"
            ></video>
            <div
              v-if="state.asset && feedback"
              class="play-feedback"
              aria-live="polite"
            >
              <button
                type="button"
                class="center-toggle"
                :disabled="!canToggle"
                :aria-label="toggleLabel"
                @click="
                  revealControls();
                  toggle();
                "
              >
                <span aria-hidden="true">
                  <RefreshCw v-if="!connected || pendingToggle !== null" />
                  <Pause v-else-if="togglePauses" /><Play v-else />
                </span>
                <b>{{ feedback }}</b>
              </button>
            </div>
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
          <div
            v-if="state.asset || wide"
            class="controls"
            :class="{ 'controls-visible': controlsVisible }"
            @pointerdown="revealControls"
            @pointerup="revealControls"
            @input="revealControls"
            @change="revealControls"
          >
            <input
              aria-label="播放进度"
              type="range"
              min="0"
              :max="duration || 1"
              step=".1"
              :value="position"
              :disabled="!duration || (!can('forward') && !can('back'))"
              :style="{
                '--buffer-track': bufferedTrack,
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
                :aria-label="togglePauses ? '暂停' : '播放'"
                :disabled="!canToggle"
                @click="toggle"
              >
                <Pause v-if="togglePauses" /><Play v-else />
              </button>
              <button
                aria-label="−10秒"
                title="后退 10 秒"
                :disabled="!state.asset || !can('back')"
                @click="control({ position: Math.max(0, target() - 10) })"
              >
                <RotateCcw /><span>10</span>
              </button>
              <button
                aria-label="+10秒"
                title="快进 10 秒"
                :disabled="!state.asset || !can('forward')"
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
              <small v-if="state.asset" class="buffer-info"
                >已缓冲 {{ Math.floor(ahead) }} 秒</small
              >
              <select v-if="state.asset" aria-label="本机画质" title="只调整本机画质" :value="quality" @change="setQuality(($event.target as HTMLSelectElement).value as Quality)">
                <option value="default">默认画质</option>
                <option value="low">省流 · 360p</option>
              </select>
              <select
                aria-label="倍速"
                :disabled="!can('rate')"
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
        <p v-if="qualityNote" role="status">{{ qualityNote }}</p>
        <form class="chat" @submit.prevent="chat">
          <label for="danmaku"><MessageSquare />弹幕</label
          ><input
            id="danmaku"
            v-model="text"
            maxlength="200"
            :disabled="!can('chat')"
            :placeholder="can('chat') ? '发一条实时弹幕' : '当前已禁言'"
          />
          <button
            :disabled="!connected || !text.trim() || !can('chat')"
            aria-label="发送"
          >
            发送<Send />
          </button>
        </form>
        <small v-if="status" class="notice" role="status">{{ status }}</small>
      </section>
      <aside class="library">
        <section class="members" aria-label="在线成员">
          <div class="list-heading">
            <h3>在线成员</h3>
            <span>到同步服务器的延迟</span>
          </div>
          <ul>
            <li v-for="person in members" :key="person.id">
              <div class="member-summary">
                <div>
                  <strong>{{ person.name }}</strong>
                  <small
                    >{{ person.role === "owner" ? "房主" : "成员"
                    }}{{ info?.self.id === person.id ? " · 我" : "" }}</small
                  >
                </div>
                <span>{{ memberProgress(person) }}</span>
              </div>
              <div class="member-side">
                <b>{{ person.rtt == null ? "待测量" : person.rtt + " ms" }}</b>
                <button
                  v-if="info?.self.role === 'owner' && person.role === 'member'"
                  aria-haspopup="dialog"
                  @click="showPermissions(person.id)"
                >
                  设置权限
                </button>
              </div>
            </li>
          </ul>
        </section>
        <div class="library-head">
          <h2>房间片源</h2>
        </div>
        <label class="pick" :class="{ busy }"
          ><input
            type="file"
            accept="video/*,.mkv"
            :disabled="busy || !can('upload')"
            @change="select"
          />
          <span class="upload-symbol"><UploadIcon /></span
          ><span>{{
            !can("upload")
              ? "上传需房主授权"
              : busy
                ? "正在处理上传"
                : "上传本地视频"
          }}</span>
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
            {{ sizeLabel(transfer.size) }}
            <span
              v-if="transfer.bytesPerSecond && transfer.phase === '正在上传'"
            >
              · 平均 {{ sizeLabel(transfer.bytesPerSecond) }}/s
            </span></small
          >
          <button v-if="transfer.phase === '正在上传'" class="invite-link" @click="pauseUpload">暂停上传</button>
          <button v-if="transfer.phase === '已暂停' || transfer.phase === '上传失败'" class="invite-link" :disabled="busy || !can('upload')" @click="runUpload()">继续上传</button>
          <small v-if="transfer.phase === '已暂停'">继续时会复用已完成的分片。</small>
          <p v-if="transfer.error" role="alert">{{ transfer.error }}</p>
        </div>
        <div class="list-heading">
          <h3>播放列表</h3>
          <div class="playlist-tools">
            <span>{{ assets.length }} 部</span>
            <button
              aria-label="刷新列表"
              title="刷新列表"
              @click="refresh().catch(fail)"
            >
              <RefreshCw />
            </button>
          </div>
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
            :disabled="!can('select')"
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
            :disabled="!can('upload')"
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
