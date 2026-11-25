export let endpoint = (
  import.meta.env.VITE_API_URL ||
  localStorage.getItem("api-origin") ||
  "http://localhost:8080"
).replace(/\/$/, "");
export function configureEndpoint(value: string) {
  const u = new URL(value);
  if (
    u.protocol !== "https:" &&
    !(u.protocol === "http:" && ["localhost", "127.0.0.1"].includes(u.hostname))
  )
    throw Error("服务地址需要 HTTPS，本机开发可用 localhost");
  if (u.username || u.password || u.pathname !== "/" || u.search || u.hash)
    throw Error("请输入服务根地址");
  if (!import.meta.env.VITE_API_URL) {
    endpoint = u.origin;
    localStorage.setItem("api-origin", endpoint);
  }
}
export type Member = {
  token: string;
  invite: string;
  room: string;
  name: string;
};
export async function api<T>(
  path: string,
  member?: Member | null,
  data?: unknown,
): Promise<T> {
  const res = await fetch(endpoint + "/api/v1/" + path, {
    method: data === undefined ? "GET" : "POST",
    headers: {
      "Content-Type": "application/json",
      ...(member ? { Authorization: "Bearer " + member.token } : {}),
    },
    body: data === undefined ? undefined : JSON.stringify(data),
  });
  if (!res.ok) throw new Error(await res.text());
  return res.json();
}
type Upload = {
  id: string;
  protocol: string;
  partSize: number;
  hashes?: Record<number, string>;
};
async function digest(bytes: BufferSource) {
  const sum = await crypto.subtle.digest("SHA-256", bytes);
  return Array.from(new Uint8Array(sum), (x) =>
    x.toString(16).padStart(2, "0"),
  ).join("");
}
export async function upload(
  file: File,
  member: Member,
  progress: (n: number) => void,
) {
  const key = [
    "upload",
    member.room,
    await digest(new TextEncoder().encode(member.token)),
    file.name,
    file.size,
    file.lastModified,
  ].join(":");
  const saved = localStorage.getItem(key);
  const job: Upload = saved
    ? JSON.parse(saved)
    : await api("uploads", member, {
        name: file.name,
        bytes: file.size,
        protocols: ["s3-multipart-v1", "kodo-multipart-v2"],
      });
  localStorage.setItem(key, JSON.stringify(job));
  if (!["s3-multipart-v1", "kodo-multipart-v2"].includes(job.protocol))
    throw Error("请更新客户端以使用当前存储");
  const path = "uploads/" + job.id + "/";
  // If completion succeeded before the response was lost, retrying it is safe.
  let parts: { PartNumber: number }[];
  try {
    parts = await api(path + "parts", member, {});
  } catch (err) {
    try {
      await api(path + "complete", member, {});
      localStorage.removeItem(key);
      return job.id;
    } catch {
      throw err;
    }
  }
  const done = new Set(parts.map((p) => p.PartNumber));
  for (let start = 0, i = 1; start < file.size; start += job.partSize, i++) {
    const chunk = file.slice(start, start + job.partSize);
    const checksum = await digest(await chunk.arrayBuffer());
    if (!done.has(i) || job.hashes?.[i] !== checksum) {
      const { url, headers } = await api<{
        url: string;
        headers?: Record<string, string>;
      }>(path + "part", member, { number: i });
      const result = await fetch(url, {
        method: "PUT",
        headers,
        body: chunk,
      });
      if (!result.ok) throw Error("分片上传失败，请重新选择同一文件继续");
      (job.hashes ??= {})[i] = checksum;
      localStorage.setItem(key, JSON.stringify(job));
    }
    progress(
      Math.min(100, Math.round(((start + job.partSize) / file.size) * 100)),
    );
  }
  await api(path + "complete", member, {});
  localStorage.removeItem(key);
  return job.id;
}
