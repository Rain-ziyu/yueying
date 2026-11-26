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
  signal?: AbortSignal,
): Promise<T> {
  const res = await fetch(endpoint + "/api/v1/" + path, {
    method: data === undefined ? "GET" : "POST",
    headers: {
      "Content-Type": "application/json",
      ...(member ? { Authorization: "Bearer " + member.token } : {}),
    },
    body: data === undefined ? undefined : JSON.stringify(data),
    signal,
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
export type UploadPhase = "准备上传" | "正在上传" | "正在校验";

function putPart(
  url: string,
  headers: Record<string, string> | undefined,
  chunk: Blob,
  signal: AbortSignal,
  report: (bytes: number) => void,
) {
  return new Promise<void>((resolve, reject) => {
    signal.throwIfAborted();
    const request = new XMLHttpRequest();
    const abort = () => request.abort();
    request.upload.onprogress = (event) =>
      report(Math.min(event.loaded, chunk.size));
    request.open("PUT", url);
    for (const [name, value] of Object.entries(headers ?? {}))
      request.setRequestHeader(name, value);
    const failed = () =>
      reject(new Error("分片上传失败，请重新选择同一文件续传"));
    request.onload = () =>
      request.status >= 200 && request.status < 300 ? resolve() : failed();
    request.onerror = request.ontimeout = failed;
    request.onabort = () =>
      reject(new DOMException("上传已中止", "AbortError"));
    request.onloadend = () => signal.removeEventListener("abort", abort);
    signal.addEventListener("abort", abort, { once: true });
    request.send(chunk);
  });
}

export async function upload(
  file: File,
  member: Member,
  progress: (phase: UploadPhase, bytes: number) => void,
  signal: AbortSignal,
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
    : await api(
        "uploads",
        member,
        {
          name: file.name,
          bytes: file.size,
          protocols: ["s3-multipart-v1", "kodo-multipart-v2"],
        },
        signal,
      );
  localStorage.setItem(key, JSON.stringify(job));
  if (!["s3-multipart-v1", "kodo-multipart-v2"].includes(job.protocol))
    throw Error("请更新客户端以使用当前存储");
  const path = "uploads/" + job.id + "/";
  // If completion succeeded before the response was lost, retrying it is safe.
  let parts: { PartNumber: number }[];
  try {
    parts = await api(path + "parts", member, {}, signal);
  } catch (err) {
    signal.throwIfAborted();
    try {
      progress("正在校验", 0);
      await api(path + "complete", member, {}, signal);
      localStorage.removeItem(key);
      return job.id;
    } catch {
      throw err;
    }
  }
  const done = new Set(parts.map((p) => p.PartNumber));
  for (let start = 0, i = 1; start < file.size; start += job.partSize, i++) {
    const chunk = file.slice(start, start + job.partSize);
    signal.throwIfAborted();
    const checksum = await digest(await chunk.arrayBuffer());
    if (!done.has(i) || job.hashes?.[i] !== checksum) {
      const { url, headers } = await api<{
        url: string;
        headers?: Record<string, string>;
      }>(path + "part", member, { number: i }, signal);
      progress("正在上传", start);
      await putPart(url, headers, chunk, signal, (bytes) =>
        progress("正在上传", start + bytes),
      );
      (job.hashes ??= {})[i] = checksum;
      localStorage.setItem(key, JSON.stringify(job));
    }
    progress("正在上传", start + chunk.size);
  }
  progress("正在校验", file.size);
  await api(path + "complete", member, {}, signal);
  localStorage.removeItem(key);
  return job.id;
}
