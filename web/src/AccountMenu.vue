<script setup lang="ts">
import { ref, onMounted, watch } from "vue";
import ProfileEditor from "./ProfileEditor.vue";
import { Check, Copy } from "@lucide/vue";
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
const editing = ref(false);
watch(
  () => session.value?.id,
  () => {
    editing.value = false;
  },
);
function profileSaved() {
  editing.value = false;
  notice.value = { kind: "success", text: "个人资料已保存" };
}
async function copyAccountId(id: string) {
  try {
    await navigator.clipboard.writeText(id);
    notice.value = { kind: "success", text: "账号 ID 已复制" };
  } catch {
    notice.value = { kind: "error", text: "复制失败，可选中账号 ID 手动复制" };
  }
}
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
  editing.value = false;
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
      <ProfileEditor
        v-if="session && editing"
        :key="session.id"
        :account="session"
        @busy="busy = $event"
        @cancel="editing = false"
        @saved="profileSaved"
      />
      <div v-else-if="session" class="profile-card">
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
          <p v-if="session.avatarUrl && brokenAvatar === session.avatarUrl">
            头像图片加载失败，已使用昵称首字
          </p>
        </div>
        <dl>
          <dt>月映账号 ID</dt>
          <dd>
            <span class="account-id-value" :title="session.id">{{
              session.id
            }}</span>
            <button
              class="profile-action"
              type="button"
              aria-label="复制完整账号 ID"
              @click="copyAccountId(session.id)"
            >
              <Check
                v-if="notice?.text === '账号 ID 已复制'"
                :size="14"
                aria-hidden="true"
              />
              <Copy v-else :size="14" aria-hidden="true" />
              {{ notice?.text === "账号 ID 已复制" ? "已复制" : "复制" }}
            </button>
          </dd>
        </dl>
        <button
          type="button"
          :disabled="busy"
          @click="
            editing = true;
            notice = undefined;
          "
        >
          编辑个人资料
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
        v-if="session && !editing"
        :disabled="busy"
        @click="run(signOut, '已退出月映账号')"
      >
        退出账号
      </button>
    </section>
  </div>
</template>
