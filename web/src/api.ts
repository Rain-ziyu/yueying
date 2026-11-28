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
  accountId?: string;
  token: string;
  invite: string;
  room: string;
  name: string;
};
class HttpError extends Error {
  constructor(readonly status: number, message: string) { super(message); }
}
export function constrainedNetwork() {
  const link = (navigator as Navigator & { connection?: { saveData?: boolean; effectiveType?: string; downlink?: number } }).connection;
  return !!link && (!!link.saveData || ["slow-2g", "2g", "3g"].includes(link.effectiveType ?? "") || (link.downlink ?? Infinity) < 2);
}
export async function api<T>(
  path: string,
  member?: Pick<Member, "token"> | null,
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
  if (!res.ok) throw new HttpError(res.status, await res.text());
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
    let stalled = false;
    let idleTimer: number;
    const watchProgress = () => {
      clearTimeout(idleTimer);
      idleTimer = window.setTimeout(() => {
        stalled = true;
        request.abort();
      }, 90000);
    };
    request.upload.onprogress = (event) => {
      watchProgress();
      report(Math.min(event.loaded, chunk.size));
    };
    request.open("PUT", url);
    for (const [name, value] of Object.entries(headers ?? {}))
      request.setRequestHeader(name, value);
    const failed = () =>
      reject(new HttpError(request.status, "分片上传失败，可继续上传"));
    request.onload = () =>
      request.status >= 200 && request.status < 300 ? resolve() : failed();
    request.onerror = request.ontimeout = failed;
    request.onabort = () =>
      stalled ? failed() : reject(new DOMException("上传已中止", "AbortError"));
    request.onloadend = () => {
      clearTimeout(idleTimer);
      signal.removeEventListener("abort", abort);
    };
    signal.addEventListener("abort", abort, { once: true });
    watchProgress();
    request.send(chunk);
  });
}

export async function upload(
  file: File,
  member: Member,
  progress: (phase: UploadPhase, bytes: number, transferred?: number) => void,
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
    parts = saved ? await api(path + "parts", member, {}, signal) : [];
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
  const count = Math.ceil(file.size / job.partSize);
  const sent = new Array<number>(count).fill(0);
  let total = 0;
  let transferred = 0;
  function report(number: number, bytes: number, resumed = false) {
    const delta = bytes - sent[number - 1];
    total += delta;
    if (!resumed) transferred += Math.max(0, delta);
    sent[number - 1] = bytes;
    progress("正在上传", total, transferred);
  }

  const batch = new AbortController();
  const stop = () => batch.abort();
  signal.throwIfAborted();
  signal.addEventListener("abort", stop, { once: true });
  let next = 1;
  async function sendOnce(number: number, chunk: Blob) {
    const { url, headers } = await api<{
      url: string;
      headers?: Record<string, string>;
    }>(path + "part", member, { number }, batch.signal);
    await putPart(url, headers, chunk, batch.signal, (bytes) =>
      report(number, bytes),
    );
  }
  async function send(number: number, chunk: Blob) {
    for (let attempt = 0; ; attempt++) {
      try {
        await sendOnce(number, chunk);
        return;
      } catch (err) {
        batch.signal.throwIfAborted();
        const transient = err instanceof TypeError || (err instanceof HttpError &&
          (err.status === 0 || err.status === 408 || err.status === 429 || err.status >= 500));
        if (!transient || attempt >= 3) throw err;
        report(number, 0);
        await new Promise<void>((resolve) => {
          const done = () => {
            clearTimeout(timer);
            batch.signal.removeEventListener("abort", done);
            resolve();
          };
          const timer = setTimeout(done, 750 * 2 ** attempt);
          batch.signal.addEventListener("abort", done, {once:true});
        });
        batch.signal.throwIfAborted();
      }
    }
  }
  async function worker() {
    while (next <= count) {
      batch.signal.throwIfAborted();
      const number = next++;
      const start = (number - 1) * job.partSize;
      const chunk = file.slice(start, start + job.partSize);
      const hash = chunk.arrayBuffer().then(digest);
      const hashes = (job.hashes ??= {});
      const previous = hashes[number];
      if (done.has(number) && previous) {
        const matches = (await hash) === previous;
        batch.signal.throwIfAborted();
        if (matches) {
          report(number, chunk.size, true);
          continue;
        }
      }
      if (previous) {
        delete hashes[number];
        localStorage.setItem(key, JSON.stringify(job));
      }
      const [checksum] = await Promise.all([hash, send(number, chunk)]);
      hashes[number] = checksum;
      localStorage.setItem(key, JSON.stringify(job));
      report(number, chunk.size);
    }
  }
  progress("正在上传", 0, 0);
  const workers = Array.from({ length: Math.min(constrainedNetwork() ? 1 : 3, count) }, worker);
  try {
    await Promise.all(workers);
  } catch (err) {
    stop();
    await Promise.allSettled(workers);
    throw err;
  } finally {
    signal.removeEventListener("abort", stop);
  }
  signal.throwIfAborted();
  progress("正在校验", file.size);
  await api(path + "complete", member, {}, signal);
  localStorage.removeItem(key);
  return job.id;
}
