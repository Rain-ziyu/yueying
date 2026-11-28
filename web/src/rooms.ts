import { api, endpoint, type Member } from "./api";

export const labels = {
  play: "播放",
  pause: "暂停",
  forward: "前进",
  back: "后退",
  chat: "发送弹幕和语音",
  upload: "上传片源",
  select: "切换片源",
  rate: "调整倍速",
};
export type Ability = keyof typeof labels;
export type Access = {
  id: string;
  role: "owner" | "legacy" | "member";
  permissions: Partial<Record<Ability, boolean>>;
};
export type RoomInfo = { title: string; listed: boolean; self: Access };
export type Saved = { member: Member; title: string; role: Access["role"] };
function prefix() {
  return "yueying-room:" + endpoint + ":";
}
export function remember(member: Member, info: RoomInfo) {
  if (member.accountId) return;
  localStorage.setItem(
    prefix() + member.invite,
    JSON.stringify({ member, title: info.title, role: info.self.role }),
  );
}
export function saved(invite: string): Saved | undefined {
  const raw = localStorage.getItem(prefix() + invite);
  if (raw) {
    try {
      return JSON.parse(raw);
    } catch {
      return;
    }
  }
}
export function forget(invite: string) {
  localStorage.removeItem(prefix() + invite);
}
export function guestRooms() {
  return Object.keys(localStorage)
    .filter((k) => k.startsWith(prefix()))
    .map((k) => saved(k.slice(prefix().length)))
    .filter((v): v is Saved => !!v && !v.member.accountId);
}
export function roomInfo(m: Member) {
  return api<RoomInfo>("room", m);
}
