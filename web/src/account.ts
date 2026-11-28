import { ref } from "vue";
import { api, endpoint, type Member } from "./api";

type Profile = {
  id: string;
  name: string;
  avatarUrl?: string;
  provider?: string;
};
type Login = Profile & { token: string };
const providerNames: Record<string, string> = {
  huawei: "华为账号",
  wechat: "微信",
  qq: "QQ",
  alipay: "支付宝",
  password: "账号密码",
};
export const providerName = (method?: string) =>
  providerNames[method || ""] || "月映账号";
export async function refreshProfile() {
  const current = readSession();
  if (!current) return;
  const profile = await accountApi<Profile>("account/me");
  const latest = readSession();
  if (
    current.token !== latest?.token ||
    current.id !== profile.id ||
    current.name !== latest.name ||
    current.avatarUrl !== latest.avatarUrl
  )
    return;
  if (
    current.name !== profile.name ||
    current.avatarUrl !== profile.avatarUrl ||
    current.provider !== profile.provider
  )
    setSession({ ...profile, token: current.token });
}
export async function saveProfile(
  accountId: string,
  patch: { name?: string; avatar?: string },
) {
  const current = readSession();
  if (!current || current.id !== accountId)
    throw Error("登录账号已变更，请重新打开个人资料");
  const updated = await accountApi<Pick<Profile, "id" | "name" | "avatarUrl">>(
    "account/me",
    patch,
  );
  if (current.token !== readSession()?.token || updated.id !== accountId)
    throw Error("登录账号已变更");
  setSession({ ...current, ...updated });
}
function sessionKey() {
  return "yueying-account:" + endpoint;
}
export function readSession(): Login | null {
  try {
    return JSON.parse(localStorage.getItem(sessionKey()) || "null");
  } catch {
    return null;
  }
}
export const session = ref<Login | null>(readSession());
window.addEventListener("yueying-service-change", () => {
  session.value = readSession();
});
window.addEventListener("storage", (e) => {
  if (!e.key || e.key === sessionKey()) session.value = readSession();
});
export function setSession(value: Login | null) {
  if (value) localStorage.setItem(sessionKey(), JSON.stringify(value));
  else localStorage.removeItem(sessionKey());
  session.value = value;
}
export async function accountApi<T>(path: string, data?: unknown) {
  const current = readSession();
  const service = endpoint;
  try {
    const result = await api<T>(path, current, data);
    if (service !== endpoint || current?.token !== readSession()?.token)
      throw Error("服务或账号已切换，请重新操作");
    return result;
  } catch (e) {
    if (
      e instanceof Error &&
      e.message.trim() === "请重新登录账号" &&
      current?.token === readSession()?.token
    )
      setSession(null);
    throw e;
  }
}
export async function signIn(
  register: boolean,
  handle: string,
  password: string,
) {
  const origin = endpoint;
  const login = await api<Login>(register ? "account/register" : "account/login", null, { handle, password });
  if (origin !== endpoint) throw Error("服务已切换，请重新登录");
  setSession(login);
}
export async function signOut() {
  const current = readSession();
  await accountApi("account/logout", {});
  if (readSession()?.token === current?.token) setSession(null);
}
export async function accountJoin(invite: string, name?: string) {
  const m = await accountApi<Member>("join", {
    invite,
    name: name || session.value?.name,
  });
  return { ...m, accountId: session.value!.id };
}
function hex(data: Uint8Array) {
  return Array.from(data, (n) => n.toString(16).padStart(2, "0")).join("");
}
const flowKey = () => sessionKey() + ":flow";
export async function startSocial(provider: string) {
  const verifier = hex(crypto.getRandomValues(new Uint8Array(32)));
  const challenge = hex(
    new Uint8Array(
      await crypto.subtle.digest("SHA-256", new TextEncoder().encode(verifier)),
    ),
  );
  const { url, state } = await accountApi<{ url: string; state: string }>(
    "account/oauth/start",
    { provider, challenge },
  );
  sessionStorage.setItem(
    flowKey(),
    JSON.stringify({
      verifier,
      state,
      invite: new URL(location.href).searchParams.get("room"),
    }),
  );
  location.assign(url);
}
export async function finishSocial() {
  const url = new URL(location.href);
  const state = url.searchParams.get("state");
  if (!state) return;
  const flow = JSON.parse(sessionStorage.getItem(flowKey()) || "null");
  const code =
    url.searchParams.get("code") || url.searchParams.get("auth_code");
  const providerError = url.searchParams.get("error");
  for (const k of [
    "code",
    "auth_code",
    "state",
    "app_id",
    "scope",
    "error",
    "error_description",
  ])
    url.searchParams.delete(k);
  if (flow?.state === state && flow.invite)
    url.searchParams.set("room", flow.invite);
  history.replaceState(null, "", url);
  if (!flow || flow.state !== state)
    throw Error("请在发起授权的浏览器中完成登录");
  sessionStorage.removeItem(flowKey());
  if (providerError === "access_denied" || (!providerError && !code))
    throw Error("已取消授权，本次操作未完成");
  if (providerError) throw Error("平台授权失败，请重试");
  const login = await accountApi<Login>("account/oauth/finish", {
    state,
    code,
    verifier: flow.verifier,
  });
  setSession(login);
  return "已使用" + providerName(login.provider) + "登录月映";
}
