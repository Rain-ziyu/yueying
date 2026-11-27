<script setup lang="ts">
import { ref, watch, onMounted } from "vue";
import { SlidersHorizontal, Users, X } from "@lucide/vue";
import { api, type Member } from "./api";
import { labels, type Ability, type Access, type RoomInfo } from "./rooms";
const props = defineProps<{
  member: Member;
  info: RoomInfo;
  focus: string;
  online: string[];
}>();
const emit = defineEmits<{ update: []; close: [] }>();
const dialog = ref<HTMLDialogElement>();
const error = ref("");
const pressedBackdrop = ref(false);
const title = ref(props.info.title),
  listed = ref(props.info.listed),
  notice = ref(""),
  busy = ref(false);
type Person = Access & { name: string };
const list = ref<Person[]>([]),
  selected = ref(props.focus);
const loading = ref(true);
let request = 0;
async function load() {
  const id = ++request;
  try {
    const result = await api<Person[]>("room/members", props.member);
    if (id === request) {
      list.value = result;
      if (!result.some((person) => person.id === selected.value))
        selected.value = result[0]?.id || "";
    }
  } catch (e) {
    if (id === request)
      error.value = e instanceof Error ? e.message : String(e);
  } finally {
    if (id === request) loading.value = false;
  }
}
watch(() => props.online.join(","), load);
watch(
  () => props.focus,
  (value) => {
    selected.value = value;
    load();
  },
);
onMounted(() => {
  dialog.value?.showModal();
  void load();
});
async function save() {
  busy.value = true;
  error.value = notice.value = "";
  try {
    await api("room/settings", props.member, {
      title: title.value,
      listed: listed.value,
    });
    notice.value = "房间设置已保存";
    emit("update");
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e);
  } finally {
    busy.value = false;
  }
}
async function change(p: Person, action: Ability) {
  busy.value = true;
  error.value = notice.value = "";
  try {
    const perms = await api<Access["permissions"]>(
      "room/permission",
      props.member,
      { id: p.id, action, allow: !p.permissions[action] },
    );
    p.permissions = perms;
    await load();
    notice.value = "成员权限已保存";
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e);
    await load();
  } finally {
    busy.value = false;
  }
}
</script>
<template>
  <dialog
    ref="dialog"
    class="management"
    aria-labelledby="management-title"
    @close="emit('close')"
    @pointerdown="pressedBackdrop = $event.target === dialog"
    @click.self="pressedBackdrop && dialog?.close()"
  >
    <div class="management-body">
      <header class="management-head">
        <SlidersHorizontal aria-hidden="true" />
        <h2 id="management-title">房间管理</h2>
        <button
          type="button"
          aria-label="关闭房间管理"
          autofocus
          @click="dialog?.close()"
        >
          <X aria-hidden="true" />
        </button>
      </header>
      <form class="manage-form" @submit.prevent="save">
        <label class="room-title-field"
          >房间名称<input v-model="title" required maxlength="60"
        /></label>
        <div class="settings-actions">
          <label class="listing-switch"
            ><input type="checkbox" v-model="listed" />在公开大厅展示</label
          >
          <button class="manage-save" :disabled="busy">保存设置</button>
        </div>
      </form>
      <section class="permission-section" aria-labelledby="permissions-heading">
        <h3 id="permissions-heading">成员权限</h3>
        <p v-if="loading" role="status">正在读取成员…</p>
        <p v-else-if="!list.length && !error" class="members-empty">
          <Users aria-hidden="true" />成员加入后，可在这里设置权限。
        </p>
        <template v-else-if="list.length"
          ><select v-model="selected" aria-label="选择成员">
            <option v-for="p in list" :key="p.id" :value="p.id">
              {{ p.name }} · {{ online.includes(p.id) ? "在线" : "离线" }} ·
              {{ p.id.slice(0, 4) }}
            </option>
          </select>
          <div
            v-for="p in list.filter((item) => item.id === selected)"
            :key="p.id"
            class="grants"
          >
            <label v-for="(label, key) in labels" :key="key" class="option"
              ><input
                type="checkbox"
                :checked="!!p.permissions[key]"
                :disabled="busy"
                @change="change(p, key)"
              />{{ label }}</label
            >
          </div>
          <small>关闭「发送弹幕」即可禁言。</small></template
        >
      </section>
      <p v-if="error" class="management-error" role="alert">{{ error }}</p>
      <p v-if="notice" role="status">{{ notice }}</p>
    </div>
  </dialog>
</template>
