// 本机递交与宿主回调分开：同会话保序，短暂故障有界重试，内存配额不随宿主运行时长增长。
import { request } from "node:http";
import { setTimeout as delay } from "node:timers/promises";

export async function emit(address, event, signal) {
  const timeout = AbortSignal.timeout(200);
  await new Promise((resolve, reject) => {
    const req = request({ socketPath: address, path: "/api/v1/events", method: "POST",
      headers: { Host: "127.0.0.1:1", "Content-Type": "application/json" },
      signal: signal ? AbortSignal.any([timeout, signal]) : timeout }, (response) => {
      const chunks = [];
      let bytes = 0;
      response.on("data", (chunk) => {
        bytes += chunk.length;
        if (bytes > 65536) { response.destroy(new Error("Bridge回执超过上限")); return; }
        chunks.push(chunk);
      });
      response.on("error", reject);
      response.on("end", () => {
        try {
          if (response.statusCode !== 202) {
            const error = new Error(`Bridge未确认接收（HTTP ${response.statusCode}）`);
            error.retryable = response.statusCode >= 500 || response.statusCode === 429;
            throw error;
          }
          const receipt = JSON.parse(Buffer.concat(chunks).toString("utf8"));
          if (receipt.event_id !== event.event_id || (receipt.accepted !== "memory" && receipt.accepted !== "filtered")) {
            throw new Error("Bridge回执无效");
          }
          resolve();
        } catch (error) { reject(error); }
      });
    });
    req.on("error", reject);
    req.end(JSON.stringify({ ...event, process_id: process.pid }));
  });
}

const MAX_EVENTS = 256;
const MAX_BYTES = 2 << 20;

export function createSender(address, signal, onError) {
  const queues = new Map();
  let count = 0, bytes = 0;
  async function drain(id, queue) {
    try {
      while (queue.length && !signal.aborted) {
        const entry = queue[0];
        for (let attempt = 0; attempt < 3; attempt++) {
          try { await emit(address, entry.event, signal); break; }
          catch (error) {
            if (signal.aborted) return;
            if (error.retryable === false || attempt === 2) { onError(error); break; }
            await delay(50 * (attempt + 1), undefined, { signal });
          }
        }
        queue.shift(); count--; bytes -= entry.bytes;
      }
    } catch (error) { if (!signal.aborted) onError(error); }
    finally {
      for (const entry of queue) { count--; bytes -= entry.bytes; }
      queues.delete(id);
    }
  }
  return (event) => {
    if (!event || signal.aborted) return;
    const size = Buffer.byteLength(JSON.stringify(event));
    if (count >= MAX_EVENTS || bytes + size > MAX_BYTES) {
      onError(new Error("本机递交队列已满，事件未接收"));
      return;
    }
    let queue = queues.get(event.session_id);
    const fresh = !queue;
    if (fresh) { queue = []; queues.set(event.session_id, queue); }
    queue.push({ event, bytes: size }); count++; bytes += size;
    if (fresh) void drain(event.session_id, queue);
  };
}
