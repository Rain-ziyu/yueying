import { ref } from "vue";
import { api, endpoint, type Member } from "./api";

type Login = { id: string; name: string; token: string };
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
  try {
    return await api<T>(path, current, data);
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
  setSession(
    await api<Login>(register ? "account/register" : "account/login", null, {
      handle,
      password,
    }),
  );
}
export async function signOut() {
  await accountApi("account/logout", {});
  setSession(null);
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
  if (!code) throw Error("授权已取消，可重新登录");
  setSession(
    await accountApi<Login>("account/oauth/finish", {
      state,
      code,
      verifier: flow.verifier,
    }),
  );
}
