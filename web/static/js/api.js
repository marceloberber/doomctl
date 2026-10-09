// Cliente da API do doomctl: JSON, CSRF, streaming de jobs (SSE) e NDJSON (IA).
let csrf = "";
let onUnauthorized = () => {};

export function setCSRF(t) { if (t) csrf = t; }
export function onAuthLost(fn) { onUnauthorized = fn; }

export class ApiError extends Error {
  constructor(status, message, data) { super(message); this.status = status; this.data = data; }
}

export async function api(method, path, body, opts = {}) {
  const headers = { Accept: "application/json" };
  if (body !== undefined) headers["Content-Type"] = "application/json";
  if (method !== "GET" && csrf) headers["X-CSRF-Token"] = csrf;
  const res = await fetch(path, {
    method, headers, credentials: "same-origin",
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
  let data = null;
  const ct = res.headers.get("content-type") || "";
  if (ct.includes("application/json")) data = await res.json().catch(() => null);
  else if (opts.raw) return res;
  if (data && data.csrf) setCSRF(data.csrf);
  if (!res.ok) {
    if (res.status === 401 && !opts.noRedirect) onUnauthorized();
    throw new ApiError(res.status, (data && data.error) || `HTTP ${res.status}`, data);
  }
  return data;
}

export const get = (p, o) => api("GET", p, undefined, o);
export const post = (p, b = {}, o) => api("POST", p, b, o);
export const put = (p, b = {}, o) => api("PUT", p, b, o);
export const del = (p, b, o) => api("DELETE", p, b, o);

// download de arquivo gerado pelo servidor (GET ou POST)
export async function download(path, body, fallbackName = "download") {
  const headers = {};
  if (body !== undefined) { headers["Content-Type"] = "application/json"; headers["X-CSRF-Token"] = csrf; }
  const res = await fetch(path, { method: body !== undefined ? "POST" : "GET", headers, credentials: "same-origin",
    body: body !== undefined ? JSON.stringify(body) : undefined });
  if (!res.ok) {
    let msg = `HTTP ${res.status}`;
    try { msg = (await res.json()).error || msg; } catch (_) { /* ignore */ }
    throw new ApiError(res.status, msg);
  }
  const cd = res.headers.get("content-disposition") || "";
  const m = /filename="([^"]+)"/.exec(cd);
  saveBlob(await res.blob(), m ? m[1] : fallbackName);
}

export function saveBlob(blob, name) {
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url; a.download = name;
  document.body.appendChild(a); a.click(); a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 2000);
}

export function saveText(text, name, type = "text/plain") { saveBlob(new Blob([text], { type }), name); }

// streamJob conecta no SSE do job: onChunk(texto), onEnd({status, exit_code, log_id, result}).
export function streamJob(jobId, { onChunk, onEnd, onMeta }) {
  const es = new EventSource(`/api/jobs/${encodeURIComponent(jobId)}/stream`);
  let ended = false;
  es.addEventListener("meta", (e) => onMeta && onMeta(JSON.parse(e.data)));
  es.addEventListener("chunk", (e) => onChunk && onChunk(JSON.parse(e.data)));
  es.addEventListener("end", (e) => { ended = true; es.close(); onEnd && onEnd(JSON.parse(e.data)); });
  es.onerror = () => {
    if (!ended) { es.close(); onEnd && onEnd({ status: "disconnected" }); }
  };
  return () => es.close();
}

// streamNDJSON faz POST e entrega cada linha JSON ao callback.
export async function streamNDJSON(path, body, onEvent, signal) {
  const res = await fetch(path, {
    method: "POST", credentials: "same-origin", signal,
    headers: { "Content-Type": "application/json", "X-CSRF-Token": csrf, Accept: "application/x-ndjson" },
    body: JSON.stringify(body),
  });
  if (!res.ok) {
    let msg = `HTTP ${res.status}`;
    try { msg = (await res.json()).error || msg; } catch (_) { /* ignore */ }
    if (res.status === 401) onUnauthorized();
    throw new ApiError(res.status, msg);
  }
  const reader = res.body.getReader();
  const dec = new TextDecoder();
  let buf = "";
  for (;;) {
    const { value, done } = await reader.read();
    if (done) break;
    buf += dec.decode(value, { stream: true });
    let i;
    while ((i = buf.indexOf("\n")) >= 0) {
      const line = buf.slice(0, i).trim();
      buf = buf.slice(i + 1);
      if (line) { try { onEvent(JSON.parse(line)); } catch (_) { /* linha parcial */ } }
    }
  }
}
