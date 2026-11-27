<script setup lang="ts">
import { ref, onMounted, watch } from "vue";
import {
  providerName,
  refreshProfile,
  accountApi,
  session,
  signIn,
  signOut,
  startSocial,
  finishSocial,
} from "./account";
const open = ref(false),
  register = ref(false),
  handle = ref(""),
  password = ref(""),
  busy = ref(false);
const notice = ref<{ kind: "success" | "error" | "pending"; text: string }>();
type Provider = { id: string; name: string; enabled: boolean };
const options = ref<Provider[]>([]),
  listError = ref(""),
  retry = ref(0);
const errorText = (e: unknown) => (e instanceof Error ? e.message : String(e));
watch([open, session, retry], async ([visible], _, onCleanup) => {
  options.value = [];
  listError.value = "";
  if (!visible || session.value) return;
  let active = true;
  onCleanup(() => {
    active = false;
  });
  try {
    const list = await accountApi<Provider[]>("account/providers");
    if (active) options.value = list;
  } catch (e) {
    if (active) listError.value = errorText(e);
  }
});
const brokenAvatar = ref("");
watch(
  () => session.value?.avatarUrl,
  () => {
    brokenAvatar.value = "";
  },
);
async function loadProfile() {
  try {
    await refreshProfile();
  } catch (e) {
    if (open.value && !busy.value)
      notice.value = { kind: "error", text: errorText(e) };
  }
}
function toggle() {
  open.value = !open.value;
  if (!busy.value) notice.value = undefined;
  if (open.value && session.value) void loadProfile();
}
async function run(fn: () => Promise<void | string>, success?: string) {
  if (busy.value) return;
  busy.value = true;
  notice.value = undefined;
  try {
    const text = (await fn()) || success;
    if (text) notice.value = { kind: "success", text };
    password.value = "";
  } catch (e) {
    notice.value = { kind: "error", text: errorText(e) };
  } finally {
    busy.value = false;
  }
}
onMounted(() => {
  if (!new URL(location.href).searchParams.has("state")) {
    void loadProfile();
    return;
  }
  open.value = true;
  void run(() => {
    notice.value = { kind: "pending", text: "正在确认授权结果…" };
    return finishSocial();
  });
});
</script>
<template>
  <div class="account-menu">
    <button
      class="invite-link account-trigger"
      :aria-expanded="open"
      :disabled="busy"
      @click="toggle"
    >
      <span v-if="session" class="profile-avatar" aria-hidden="true">
        <img
          v-if="session.avatarUrl && brokenAvatar !== session.avatarUrl"
          :src="session.avatarUrl"
          alt=""
          referrerpolicy="no-referrer"
          @error="brokenAvatar = session.avatarUrl"
        />
        <template v-else>{{ Array.from(session.name)[0] }}</template>
      </span>
      <span>{{ session ? session.name : "账号登录" }}</span>
    </button>
    <section v-if="open" class="account-panel" aria-label="账号">
      <h3>
        {{ session ? "个人资料" : register ? "注册月映账号" : "登录月映" }}
      </h3>
      <p
        v-if="notice"
        class="account-notice"
        :class="notice.kind"
        :role="notice.kind === 'error' ? 'alert' : 'status'"
      >
        {{ notice.text }}
      </p>
      <div v-if="session" class="profile-card">
        <span class="profile-avatar large" aria-hidden="true">
          <img
            v-if="session.avatarUrl && brokenAvatar !== session.avatarUrl"
            :src="session.avatarUrl"
            alt=""
            referrerpolicy="no-referrer"
            @error="brokenAvatar = session.avatarUrl"
          />
          <template v-else>{{ Array.from(session.name)[0] }}</template>
        </span>
        <div>
          <strong>{{ session.name }}</strong>
          <p>{{ providerName(session.provider) }}登录</p>
          <p v-if="session.provider === 'huawei' && !session.avatarUrl">暂无华为头像，已使用昵称首字</p>
          <p v-else-if="session.avatarUrl && brokenAvatar === session.avatarUrl">头像图片加载失败，已使用昵称首字</p>
        </div>
        <dl>
          <dt>月映账号 ID</dt>
          <dd>{{ session.id }}</dd>
        </dl>
        <button
          v-if="session.provider === 'huawei'"
          type="button"
          :disabled="busy"
          @click="run(() => startSocial('huawei'))"
        >
          从华为更新头像和昵称
        </button>
      </div>
      <form
        v-if="!session"
        @submit.prevent="
          run(
            () => signIn(register, handle, password),
            register ? '账号已创建并登录' : '登录成功',
          )
        "
      >
        <label
          >账号<input
            v-model="handle"
            required
            maxlength="32"
            autocomplete="username"
            placeholder="3–32 位字母、数字或下划线"
        /></label>
        <label
          >密码<input
            v-model="password"
            required
            type="password"
            :autocomplete="register ? 'new-password' : 'current-password'"
            placeholder="至少 10 字节"
        /></label>
        <button class="manage-save" :disabled="busy">
          {{ register ? "注册并登录" : "登录" }}
        </button>
        <button
          type="button"
          :disabled="busy"
          @click="
            register = !register;
            notice = undefined;
          "
        >
          {{ register ? "已有账号" : "注册账号" }}
        </button>
      </form>
      <template v-if="!session">
        <h4>其他登录方式</h4>
        <p v-if="listError" role="alert">
          {{ listError }}
          <button type="button" @click="retry++">重新加载</button>
        </p>
        <p v-else-if="!options.length">正在读取登录方式…</p>
        <div class="providers">
          <button
            v-for="p in options"
            :key="p.id"
            :disabled="busy || !p.enabled"
            @click="run(() => startSocial(p.id))"
          >
            {{ p.name }}<small>{{ p.enabled ? "登录" : "待开通" }}</small>
          </button>
        </div>
      </template>
      <button
        v-if="session"
        :disabled="busy"
        @click="run(signOut, '已退出月映账号')"
      >
        退出账号
      </button>
    </section>
  </div>
</template>
