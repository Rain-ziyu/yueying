<script setup lang="ts">
import { ref, onMounted, watch } from "vue";
import { ArrowRight, Plus, DoorOpen, Compass, Library, Search } from "@lucide/vue";
import { api, configureEndpoint, endpoint, type Member } from "./api";
import { session, accountApi, accountJoin } from "./account";
import { guestRooms, saved, remember, forget } from "./rooms";
const emit = defineEmits<{
  enter: [member: Member];
  error: [error: unknown];
}>();
const invite = ref(new URLSearchParams(location.search).get("room") || "");
const tab = ref<"create" | "join" | "browse" | "mine">(
  invite.value ? "join" : "create",
);
const tabs = [
  { id: "create", icon: Plus, label: "创建房间" },
  { id: "join", icon: DoorOpen, label: "加入房间" },
  { id: "browse", icon: Compass, label: "公开大厅" },
  { id: "mine", icon: Library, label: "我的房间" },
] as const;
const name = ref(""),
  title = ref(""),
  listed = ref(false),
  busy = ref(false),
  serverUrl = ref(endpoint);
const connectionNote = ref("");
function saveConnection() {
  try {
    configureEndpoint(serverUrl.value.trim());
    serverUrl.value = endpoint;
    connectionNote.value = "服务地址已保存";
  } catch (e) {
    connectionNote.value = e instanceof Error ? e.message : "服务地址无效";
  }
}
const cinematicMotion = window.matchMedia("(hover: hover) and (pointer: fine) and (prefers-reduced-motion: no-preference)");
function moveScene(event: PointerEvent) {
  if (!cinematicMotion.matches || event.pointerType === "touch") return;
  const poster = event.currentTarget as HTMLElement;
  const bounds = poster.getBoundingClientRect();
  const x = (event.clientX - bounds.left) / bounds.width - 0.5;
  const y = (event.clientY - bounds.top) / bounds.height - 0.5;
  poster.style.setProperty("--pan-x", (x * 14).toFixed(1) + "px");
  poster.style.setProperty("--pan-y", (y * 10).toFixed(1) + "px");
}
function resetScene(event: PointerEvent) {
  const poster = event.currentTarget as HTMLElement;
  poster.style.removeProperty("--pan-x");
  poster.style.removeProperty("--pan-y");
}
const configurable = !import.meta.env.VITE_API_URL;
const publicRooms = ref<{ title: string; invite: string }[]>([]),
  more = ref(false);
type Card = { title: string; invite: string; owner: boolean; name: string };
const cloud = ref<Card[]>([]),
  guests = ref<ReturnType<typeof guestRooms>>([]);
let listVersion = 0;
async function loadMine() {
  const version = ++listVersion;
  cloud.value = [];
  guests.value = [];
  busy.value = true;
  try {
    const saved = guestRooms();
    const cards = session.value
      ? await accountApi<Card[]>("account/rooms")
      : [];
    const verified = [] as typeof saved;
    for (let i = 0; i < saved.length; i += 100) {
      const batch = saved.slice(i, i + 100);
      const rows = await api<Card[]>("account/guests", null, {
        tokens: batch.map((g) => g.member.token),
      });
      for (const room of batch) {
        const card = rows.find((v) => v.invite === room.member.invite);
        if (card) verified.push({ ...room, title: card.title });
        else forget(room.member.invite);
      }
    }
    if (version === listVersion) {
      cloud.value = cards;
      guests.value = verified.filter(
        (g) => session.value || g.role === "owner",
      );
    }
  } catch (e) {
    emit("error", e);
  } finally {
    if (version === listVersion) busy.value = false;
  }
}
async function bind(g: ReturnType<typeof guestRooms>[number]) {
  try {
    await accountApi("account/claim", { guest: g.member.token });
    forget(g.member.invite);
    await loadMine();
  } catch (e) {
    emit("error", e);
  }
}
async function openCloud(r: Card) {
  try {
    emit("enter", await accountJoin(r.invite, r.name));
  } catch (e) {
    emit("error", e);
  }
}
watch(session, () => {
  const restored = new URL(location.href).searchParams.get("room");
  if (restored && !invite.value) {
    invite.value = restored;
    tab.value = "join";
  }
  name.value = session.value?.name || "";
  if (tab.value === "mine") loadMine();
});

const query = ref("");
let browseVersion = 0;
watch(query, (_, __, onCleanup) => {
  ++browseVersion;
  publicRooms.value = [];
  more.value = false;
  busy.value = true;
  const timer = window.setTimeout(() => browse(), 250);
  onCleanup(() => clearTimeout(timer));
});
async function browse(append = false) {
  const version = ++browseVersion;
  busy.value = true;
  try {
    configureEndpoint(serverUrl.value);
    const last = append ? publicRooms.value.at(-1)?.invite : "";
    const params = new URLSearchParams();
    if (query.value.trim()) params.set("q", query.value.trim());
    if (last) params.set("after", last);
    const res = await api<{ rooms: typeof publicRooms.value; more: boolean }>(
      "rooms?" + params,
    );
    if (version !== browseVersion) return;
    publicRooms.value = append
      ? [...publicRooms.value, ...res.rooms]
      : res.rooms;
    more.value = res.more;
  } catch (e) {
    if (version === browseVersion) emit("error", e);
  } finally {
    if (version === browseVersion) busy.value = false;
  }
}
function switchTab(t: typeof tab.value) {
  tab.value = t;
  if (t === "browse") browse();
  if (t === "mine") loadMine();
}
async function submit() {
  if (busy.value) return;
  busy.value = true;
  try {
    configureEndpoint(serverUrl.value);
    const joining = tab.value === "join",
      code = invite.value.trim();
    const data = {
      name: name.value.trim(),
      invite: code,
      title: title.value.trim(),
      listed: listed.value,
    };
    let m: Member;
    if (session.value) {
      if (joining && saved(code))
        throw Error("此浏览器已有该房间的访客身份，请先在「我的房间」绑定");
      m = await accountApi<Member>(joining ? "join" : "rooms", data);
      m.accountId = session.value.id;
    } else {
      m =
        (joining ? saved(code)?.member : undefined) ??
        (await api<Member>(joining ? "join" : "rooms", null, data));
      if (!joining || !saved(code))
        remember(m, {
          title: title.value.trim() || "观影房间",
          listed: listed.value,
          self: { id: "", role: joining ? "member" : "owner", permissions: {} },
        });
    }
    emit("enter", m);
  } catch (e) {
    emit("error", e);
  } finally {
    busy.value = false;
  }
}
function pick(code: string) {
  invite.value = code;
  tab.value = "join";
  name.value = saved(code)?.member.name || name.value;
}
onMounted(() => {
  name.value = session.value?.name || saved(invite.value)?.member.name || "";
});
</script>
<template>
  <section class="lobby shell">
    <div class="poster" @pointermove="moveScene" @pointerleave="resetScene" @pointercancel="resetScene">
      <div class="cinema-panorama" aria-hidden="true"></div>
      <div class="poster-caption">
        <h1>一起看部电影<span>。</span></h1>
        <p>与朋友共享画面、进度和实时弹幕。</p>
      </div>
    </div>
    <div class="entry">
      <nav class="lobby-nav" aria-label="房间导航">
        <button
          v-for="t in tabs"
          :key="t.id"
          :aria-current="t.id === tab ? 'page' : undefined"
          :disabled="busy"
          @click="switchTab(t.id)"
        >
          <component :is="t.icon" aria-hidden="true" /> {{ t.label }}
        </button>
      </nav>
      <div :key="tab" class="lobby-content">
      <h2>{{ tabs.find((t) => t.id === tab)?.label }}</h2>
      <form
        v-if="tab === 'create' || tab === 'join'"
        class="entry-form"
        @submit.prevent="submit"
      >
        <label
          >你的昵称<input
            v-model="name"
            required
            maxlength="40"
            placeholder="朋友怎么称呼你"
        /></label>
        <p v-if="!session && tab === 'create'" class="retention-note">
          访客房间连续 30 天无人使用后自动清理。登录可长期保留。
        </p>
        <template v-if="tab === 'create'"
          ><label
            >房间名称<input
              v-model="title"
              maxlength="60"
              placeholder="观影房间"
          /></label>
          <label class="option"
            ><input type="checkbox" v-model="listed" />在公开大厅展示</label
          ></template
        >
        <label v-else
          >房间码<input
            v-model="invite"
            required
            placeholder="输入朋友分享的房间码"
        /></label>
        <details v-if="configurable">
          <summary>连接设置</summary>
          <label
            >月映服务地址<input v-model="serverUrl" @input="connectionNote = ''" type="url" required
          /></label>
          <button type="button" class="save-endpoint" @click="saveConnection">保存服务地址</button>
          <p v-if="connectionNote" role="status">{{ connectionNote }}</p>
        </details>
        <button class="primary" :disabled="busy">
          {{ tab === "create" ? "创建房间" : "加入房间" }}<ArrowRight />
        </button>
      </form>
      <template v-else-if="tab === 'browse'"
        ><label class="room-search"><Search aria-hidden="true" /><input v-model="query" type="search" aria-label="查找公开房间" placeholder="输入房间名称或房间码" maxlength="100" /></label>
        <p role="status" v-if="!publicRooms.length">
          {{ busy ? "正在加载…" : query.trim() ? "没有匹配的公开房间" : "暂无公开房间" }}
        </p>
        <button
          class="room-row"
          v-for="r in publicRooms"
          :key="r.invite"
          @click="pick(r.invite)"
        >
          <strong>{{ r.title }}<small>{{ r.invite }}</small></strong><span>加入 →</span></button
        ><button v-if="more" :disabled="busy" @click="browse(true)">
          加载更多
        </button></template
      >
      <template v-else>
        <p v-if="busy">正在读取云端房间…</p>
        <p v-else-if="!guests.length && !cloud.length">暂无房间。</p>
        <button
          class="room-row"
          v-for="r in cloud"
          :key="r.invite"
          @click="openCloud(r)"
        >
          {{ r.title }}<span>{{ r.owner ? "进入房间" : "重新加入" }} →</span>
        </button>
        <p v-if="guests.length">本机访客身份</p>
        <div v-for="g in guests" :key="g.member.room" class="guest-row">
          <button
            class="room-row"
            @click="emit('enter', g.member)"
          >
            {{ g.title
            }}<span>{{ g.role === "owner" ? "房主" : "成员" }} →</span>
          </button>
          <button v-if="session" class="invite-link" @click="bind(g)">
            绑定到此账号
          </button>
        </div>
      </template>
      </div>
    </div>
  </section>
</template>
