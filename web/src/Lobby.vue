<script setup lang="ts">
import { ref, onMounted } from "vue";
import { ArrowRight } from "@lucide/vue";
import { api, configureEndpoint, endpoint, type Member } from "./api";
import { owned, saved, remember } from "./rooms";
const emit = defineEmits<{
  enter: [member: Member, manage?: boolean];
  error: [error: unknown];
}>();
const invite = ref(new URLSearchParams(location.search).get("room") || "");
const tab = ref<"create" | "join" | "browse" | "mine">(
  invite.value ? "join" : "create",
);
const tabs = [
  { id: "create", label: "创建房间" },
  { id: "join", label: "加入房间" },
  { id: "browse", label: "公开大厅" },
  { id: "mine", label: "我的房间" },
] as const;
const name = ref(""),
  title = ref(""),
  listed = ref(false),
  busy = ref(false),
  serverUrl = ref(endpoint);
const configurable = !import.meta.env.VITE_API_URL;
const publicRooms = ref<{ title: string; invite: string }[]>([]),
  more = ref(false);
const myRooms = ref(owned());
async function browse(append = false) {
  busy.value = true;
  try {
    configureEndpoint(serverUrl.value);
    const last = append ? publicRooms.value.at(-1)?.invite : "";
    const res = await api<{ rooms: typeof publicRooms.value; more: boolean }>(
      "rooms" + (last ? "?after=" + encodeURIComponent(last) : ""),
    );
    publicRooms.value = append
      ? [...publicRooms.value, ...res.rooms]
      : res.rooms;
    more.value = res.more;
  } catch (e) {
    emit("error", e);
  } finally {
    busy.value = false;
  }
}
function switchTab(t: typeof tab.value) {
  tab.value = t;
  if (t === "browse") browse();
  if (t === "mine") myRooms.value = owned();
}
async function submit() {
  if (busy.value) return;
  busy.value = true;
  try {
    configureEndpoint(serverUrl.value);
    const existing =
      tab.value === "join" ? saved(invite.value.trim()) : undefined;
    const m =
      existing?.member ??
      (await api<Member>(tab.value === "create" ? "rooms" : "join", null, {
        name: name.value.trim(),
        invite: invite.value.trim(),
        title: title.value.trim(),
        listed: listed.value,
      }));
    if (!existing)
      remember(m, {
        title: title.value.trim() || "观影房间",
        listed: listed.value,
        self: {
          id: "",
          role: tab.value === "create" ? "owner" : "member",
          permissions: {},
        },
      });
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
  name.value = saved(invite.value)?.member.name || "";
});
</script>
<template>
  <section class="lobby shell">
    <div class="poster">
      <div class="lunar" aria-hidden="true"></div>
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
          {{ t.label }}
        </button>
      </nav>
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
            >月映服务地址<input v-model="serverUrl" type="url" required
          /></label>
        </details>
        <button class="primary" :disabled="busy">
          {{ tab === "create" ? "创建房间" : "加入房间" }}<ArrowRight />
        </button>
      </form>
      <template v-else-if="tab === 'browse'"
        ><p v-if="!publicRooms.length">
          {{ busy ? "正在加载…" : "暂无公开房间" }}
        </p>
        <button
          class="room-row"
          v-for="r in publicRooms"
          :key="r.invite"
          @click="pick(r.invite)"
        >
          {{ r.title }}<span>加入 →</span></button
        ><button v-if="more" :disabled="busy" @click="browse(true)">
          加载更多
        </button></template
      >
      <template v-else
        ><p>这里保存在此浏览器创建的房间。</p>
        <p v-if="!myRooms.length">还没有创建房间。</p>
        <button
          class="room-row"
          v-for="r in myRooms"
          :key="r.member.room"
          @click="emit('enter', r.member, true)"
        >
          {{ r.title }}<span>进入管理 →</span>
        </button></template
      >
    </div>
  </section>
</template>
