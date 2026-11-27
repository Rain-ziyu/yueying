<script setup lang="ts">
import { ref, onMounted } from "vue";
import { api } from "./api";
import { session, signIn, signOut, startSocial, finishSocial } from "./account";
const emit = defineEmits<{ error: [e: unknown] }>();
const open = ref(false),
  register = ref(false),
  handle = ref(""),
  password = ref(""),
  busy = ref(false),
  message = ref("");
const options = ref<{ id: string; name: string; enabled: boolean }[]>([]);
async function toggle() {
  open.value = !open.value;
  message.value = "";
  if (open.value)
    try {
      options.value = await api("account/providers");
    } catch (e) {
      emit("error", e);
    }
}
async function run(fn: () => Promise<void>) {
  busy.value = true;
  message.value = "";
  try {
    await fn();
    open.value = false;
    password.value = "";
  } catch (e) {
    message.value = e instanceof Error ? e.message : String(e);
  } finally {
    busy.value = false;
  }
}
onMounted(() => finishSocial().catch((e) => emit("error", e)));
</script>
<template>
  <div class="account-menu">
    <button class="invite-link" :aria-expanded="open" @click="toggle">
      {{ session ? session.name : "账号登录" }}
    </button>
    <section v-if="open" class="account-panel" aria-label="账号">
      <h3>
        {{ session ? "账号管理" : register ? "注册月映账号" : "登录月映" }}
      </h3>
      <form
        v-if="!session"
        @submit.prevent="run(() => signIn(register, handle, password))"
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
          @click="
            register = !register;
            message = '';
          "
        >
          {{ register ? "已有账号" : "注册账号" }}
        </button>
      </form>
      <h4>{{ session ? "绑定第三方登录" : "其他登录方式" }}</h4>
      <div class="providers">
        <button
          v-for="p in options"
          :key="p.id"
          :disabled="busy || !p.enabled"
          @click="run(() => startSocial(p.id))"
        >
          {{ p.name }}<small v-if="!p.enabled">待开通</small>
        </button>
      </div>
      <button v-if="session" :disabled="busy" @click="run(signOut)">
        退出账号
      </button>
      <p v-if="message" role="alert">{{ message }}</p>
    </section>
  </div>
</template>
