<script setup lang="ts">
import { computed, ref } from "vue";
import { ImagePlus, RotateCcw } from "@lucide/vue";
import { saveProfile } from "./account";

const props = defineProps<{
  account: { id: string; name: string; avatarUrl?: string };
}>();
const emit = defineEmits<{ saved: []; cancel: []; busy: [boolean] }>();
const originalName = props.account.name;
const name = ref(originalName),
  avatar = ref<string>(),
  processing = ref(false),
  error = ref("");
const preview = computed(() => avatar.value ?? props.account.avatarUrl);
const brokenPreview = ref("");
const changed = computed(
  () =>
    name.value.trim() !== originalName ||
    (avatar.value !== undefined &&
      avatar.value !== (props.account.avatarUrl || "")),
);
function setBusy(value: boolean) {
  processing.value = value;
  emit("busy", value);
}
async function choose(event: Event) {
  const input = event.target as HTMLInputElement;
  const file = input.files?.[0];
  input.value = "";
  if (!file) return;
  error.value = "";
  if (file.size > 12 * 1024 * 1024) {
    error.value = "请选择 12 MB 以内的图片";
    return;
  }
  setBusy(true);
  const url = URL.createObjectURL(file);
  try {
    const img = new Image();
    await new Promise<void>((resolve, reject) => {
      img.onload = () => resolve();
      img.onerror = reject;
      img.src = url;
    });
    const side = Math.min(img.naturalWidth, img.naturalHeight);
    const canvas = document.createElement("canvas");
    canvas.width = canvas.height = Math.min(192, side);
    const ctx = canvas.getContext("2d");
    if (!ctx || !side) throw Error();
    ctx.fillStyle = "#fff";
    ctx.fillRect(0, 0, canvas.width, canvas.height);
    ctx.drawImage(
      img,
      (img.naturalWidth - side) / 2,
      (img.naturalHeight - side) / 2,
      side,
      side,
      0,
      0,
      canvas.width,
      canvas.height,
    );
    avatar.value = canvas.toDataURL("image/jpeg", 0.88);
    brokenPreview.value = "";
  } catch {
    error.value = "无法读取这张图片，请选择 JPG、PNG 或 WebP 图片";
  } finally {
    URL.revokeObjectURL(url);
    setBusy(false);
  }
}
async function save() {
  if (processing.value || !changed.value) return;
  error.value = "";
  setBusy(true);
  try {
    const trimmed = name.value.trim();
    if (!trimmed || Array.from(trimmed).length > 40)
      throw Error("昵称需为 1–40 个字符");
    await saveProfile(props.account.id, {
      name: trimmed !== originalName ? trimmed : undefined,
      avatar: avatar.value,
    });
    emit("saved");
  } catch (e) {
    error.value = e instanceof Error ? e.message : "保存失败，请重试";
  } finally {
    setBusy(false);
  }
}
</script>
<template>
  <form class="profile-editor" @submit.prevent="save">
    <div class="avatar-editor-row">
      <span class="profile-avatar large" aria-label="头像预览">
        <img
          v-if="preview && brokenPreview !== preview"
          :src="preview"
          alt="头像预览"
          referrerpolicy="no-referrer"
          @error="brokenPreview = preview"
        />
        <template v-else>{{ Array.from(name.trim())[0] }}</template>
      </span>
      <div class="avatar-controls">
        <label class="profile-action avatar-picker">
          <ImagePlus :size="15" aria-hidden="true" />选择图片
          <input
            aria-label="选择头像图片"
            :disabled="processing"
            type="file"
            accept="image/jpeg,image/png,image/webp"
            @change="choose"
          />
        </label>
        <button
          class="profile-action"
          type="button"
          :disabled="processing || !preview"
          title="恢复昵称首字头像"
          @click="
            avatar = '';
            error = '';
          "
        >
          <RotateCcw :size="15" aria-hidden="true" />重置头像
        </button>
      </div>
    </div>
    <label
      >月映昵称<input
        v-model="name"
        required
        :disabled="processing"
        autocomplete="nickname"
    /></label>
    <p v-if="error" role="alert" class="account-notice error">{{ error }}</p>
    <button
      class="manage-save"
      type="submit"
      :disabled="processing || !changed"
    >
      保存资料
    </button>
    <button type="button" :disabled="processing" @click="emit('cancel')">
      取消
    </button>
  </form>
</template>
