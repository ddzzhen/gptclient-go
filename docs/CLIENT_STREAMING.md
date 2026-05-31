# 客户端流式接入指南

本文档说明如何通过 `/v1/chat/completions` 同时接收：

- **消息正文**（`choices[0].delta.content`）
- **产物侧信道**（`sentinel`：生图、沙箱文件、进度）
- **最终图片**（按图位槽位与修订策略）

适用于 OpenAI 兼容客户端自行实现 UI，或业务侧解析结构化 JSON 回复。

---

## 1. 双通道模型

同一条 SSE 流里有两类数据，**不要混用**：

| 通道 | JSON 路径 | 用途 |
|------|-----------|------|
| **正文** | `choices[0].delta.content` | 助手文字；识图等场景常为拼完后 `JSON.parse` 的业务 JSON |
| **产物** | `sentinel` | 生图、沙箱 PDF/文件、生成进度 |

```text
POST /v1/chat/completions (stream=true)
        │
        ├─► delta.content     ──► 累加 → 展示 / JSON.parse
        │
        └─► sentinel          ──► 图片预览、文件下载链接、进度文案
        │
        └─► finish_reason=stop + conversation_id
        └─► data: [DONE]
```

---

## 2. 推荐请求

### 2.1 生图 + 文件（推荐）

```json
{
  "model": "gpt-4o",
  "stream": true,
  "messages": [
    { "role": "user", "content": "画一张产品图" }
  ],
  "conversation_id": "",
  "include_thinking": false,
  "artifact_delivery": "url",
  "artifact_image_revisions": "latest_per_slot",
  "artifact_markdown": false
}
```

### 2.2 扩展字段说明

| 字段 | 默认值 | 说明 |
|------|--------|------|
| `conversation_id` | 空 | 多轮续聊；首次留空，响应 `stop` chunk 带回 |
| `include_thinking` | `false` | `true` 时正文含 `\x00THINK\x00` 等（内置 Web UI）；业务客户端保持 `false` |
| `artifact_delivery` | `url` | `url` \| `base64` \| `base64_chunked` |
| `artifact_image_revisions` | `latest_per_slot` | 见 [§5 最终图片](#5-最终图片) |
| `artifact_base64_chunk_size` | `393216` | `base64_chunked` 每块原始字节数 |
| `artifact_markdown` | `false` | `true` 时在正文末尾追加 markdown 图片/文件链接（旧客户端兼容） |

### 2.3 鉴权

与 OpenAI 相同：

```http
Authorization: Bearer <API Token 或 ChatGPT accessToken>
Content-Type: application/json
```

详见项目根目录 [API.md](../API.md)。

---

## 3. SSE  wire 格式

### 3.1 典型顺序

```text
data: {"choices":[{"delta":{"role":"assistant"}}]}

data: {"choices":[{"delta":{"content":"你"}}]}
data: {"choices":[{"delta":{"content":"好"}}]}

data: {"choices":[{"delta":{}}],"sentinel":{"event":"artifact_pending","kind":"generated_image","title":"正在生成图片..."}}

data: {"choices":[{"delta":{}}],"sentinel":{"event":"artifact","kind":"generated_image","slot_index":1,"revision":1,"file_id":"file-xxx","url":"http://..."}}

data: {"choices":[{"delta":{}}],"sentinel":{"event":"artifact_superseded","kind":"generated_image","slot_index":1,"file_id":"file-old",...}}

data: {"choices":[{"delta":{}}],"sentinel":{"event":"artifact","kind":"generated_image","slot_index":1,"revision":2,"file_id":"file-yyy","url":"http://..."}}

data: {"choices":[{"delta":{}}],"sentinel":{"event":"artifact_slot_final","kind":"generated_image","slot_index":1,"file_id":"file-yyy",...}}

data: {"choices":[{"delta":{},"finish_reason":"stop"}],"conversation_id":"conv-xxx"}

data: [DONE]
```

### 3.2 解析规则

1. 按行读取，只处理以 `data: ` 开头的行。
2. `data: [DONE]` 表示流结束。
3. 其余 `data: {...}` 解析为 JSON；**不要**假设每条都是完整业务 JSON。
4. `conversation_id` 出现在 **`finish_reason: "stop"`** 的 chunk 上。
5. 正文 = 所有 `choices[0].delta.content` 的拼接（默认已过滤思考块）。

### 3.3 Chunk 结构（流式）

```json
{
  "id": "chatcmpl-...",
  "object": "chat.completion.chunk",
  "created": 1710000000,
  "model": "gpt-4o",
  "choices": [{
    "index": 0,
    "delta": { "role": "assistant", "content": "..." },
    "finish_reason": null
  }],
  "conversation_id": "",
  "sentinel": { "event": "artifact", "...": "..." }
}
```

---

## 4. `sentinel` 事件参考

### 4.1 事件类型

| `event` | 说明 |
|---------|------|
| `artifact_pending` | 进度提示（如「正在生成图片…」） |
| `artifact` | 产物就绪（图或文件） |
| `artifact_superseded` | 同图位旧版被替换（`latest_per_slot`） |
| `artifact_slot_final` | **该图位已定稿**（约 4s 无新图后） |
| `artifact_chunk` | `base64_chunked` 数据块 |
| `artifact_done` | 分块传输完成 |

### 4.2 生图 `artifact` 示例

```json
{
  "event": "artifact",
  "kind": "generated_image",
  "slot_index": 1,
  "revision": 3,
  "gen_id": "gen_abc",
  "message_id": "msg_xyz",
  "file_id": "file-service://...",
  "url": "http://127.0.0.1:5005/api/image/proxy?conv_id=...&file_id=...",
  "mime_type": "image/png",
  "name": "generated_slot1_rev3.png",
  "is_final": false,
  "supersedes_file_id": "file-old-id",
  "update_type": "async-task-update-message"
}
```

| 字段 | 说明 |
|------|------|
| `slot_index` | 图位编号（图1、图2…） |
| `revision` | 该图位内第几次更新（过程图 1→2→3→4） |
| `gen_id` | DALL·E 生成链 ID |
| `supersedes_file_id` | 被本张替换的旧 `file_id` |
| `is_final` | `final_only` 模式或收尾时为 `true` |

### 4.3 沙箱文件 `artifact` 示例

```json
{
  "event": "artifact",
  "kind": "sandbox_file",
  "name": "report.pdf",
  "message_id": "msg-...",
  "sandbox_path": "/mnt/data/report.pdf",
  "url": "http://127.0.0.1:5005/api/pdf/proxy?conv_id=...&msg_id=...&sandbox_path=...",
  "mime_type": "application/pdf",
  "size_bytes": 12345
}
```

### 4.4 产物下发方式 `artifact_delivery`

| 值 | `artifact` 内容 |
|----|-----------------|
| `url` | 带 `url`，由服务端代理 `/api/image/proxy`、`/api/pdf/proxy` |
| `base64` | 单条 `data`（标准 base64） |
| `base64_chunked` | 多条 `artifact_chunk`，最后 `artifact_done` |

---

## 5. 最终图片

`artifact_image_revisions` 控制多版本过程图如何推送：

| 值 | 行为 | 客户端如何得到「最终图」 |
|----|------|--------------------------|
| `latest_per_slot`（**默认**） | 每图位只保留最新；旧图发 `artifact_superseded` | 每个 `slot_index` 最后一次 `artifact` 的 `url` / `file_id` |
| `final_only` | 图位 idle 结束后才推 `artifact`，且 `is_final: true` | 只处理 `is_final: true` 的 `artifact` |
| `all` | 每个中间 `file_id` 都推 | 每 `slot_index` 取 `revision` 最大的一条 |

**定稿信号（建议同时监听）：**

- `event === "artifact_slot_final"` → 该 `slot_index` 不再更新
- 收到 `data: [DONE]` 后，按槽位排序的当前图列表即为最终展示列表

---

## 6. TypeScript 参考实现

```typescript
export interface SentinelEvent {
  event: string;
  kind?: "generated_image" | "sandbox_file";
  slot_index?: number;
  revision?: number;
  gen_id?: string;
  file_id?: string;
  url?: string;
  name?: string;
  message_id?: string;
  sandbox_path?: string;
  mime_type?: string;
  data?: string;
  chunk_index?: number;
  chunk_total?: number;
  is_final?: boolean;
  supersedes_file_id?: string;
  title?: string;
  error?: string;
}

export interface SlotImage {
  slotIndex: number;
  revision: number;
  fileId: string;
  url?: string;
  final: boolean;
}

export interface StreamResult {
  content: string;
  conversationId?: string;
  images: SlotImage[];
  files: Array<{ name: string; url?: string; messageId?: string; path?: string }>;
  sentinelLog: SentinelEvent[];
}

type Chunk = {
  choices?: Array<{
    delta?: { role?: string; content?: string };
    finish_reason?: string | null;
  }>;
  conversation_id?: string;
  sentinel?: SentinelEvent;
};

export async function chatStream(
  baseUrl: string,
  apiKey: string,
  body: Record<string, unknown>,
  onUpdate?: (partial: StreamResult) => void,
): Promise<StreamResult> {
  const slots = new Map<number, SlotImage>();
  const files: StreamResult["files"] = [];
  const sentinelLog: SentinelEvent[] = [];
  let content = "";
  let conversationId: string | undefined;

  const resp = await fetch(`${baseUrl}/v1/chat/completions`, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      Authorization: `Bearer ${apiKey}`,
    },
    body: JSON.stringify({
      stream: true,
      include_thinking: false,
      artifact_delivery: "url",
      artifact_image_revisions: "latest_per_slot",
      artifact_markdown: false,
      ...body,
    }),
  });

  if (!resp.ok || !resp.body) {
    throw new Error(`HTTP ${resp.status}`);
  }

  const reader = resp.body.getReader();
  const dec = new TextDecoder();
  let buf = "";

  const snapshot = (): StreamResult => ({
    content,
    conversationId,
    images: [...slots.values()].sort((a, b) => a.slotIndex - b.slotIndex),
    files: [...files],
    sentinelLog: [...sentinelLog],
  });

  const handleSentinel = (ev: SentinelEvent) => {
    sentinelLog.push(ev);

    if (ev.kind === "generated_image") {
      if (ev.event === "artifact_superseded") return;
      if (ev.event === "artifact" && ev.slot_index != null && ev.file_id) {
        slots.set(ev.slot_index, {
          slotIndex: ev.slot_index,
          revision: ev.revision ?? 0,
          fileId: ev.file_id,
          url: ev.url,
          final: !!ev.is_final,
        });
      }
      if (ev.event === "artifact_slot_final" && ev.slot_index != null) {
        const cur = slots.get(ev.slot_index);
        if (cur) slots.set(ev.slot_index, { ...cur, final: true });
      }
      return;
    }

    if (ev.kind === "sandbox_file" && ev.event === "artifact") {
      files.push({
        name: ev.name ?? "file",
        url: ev.url,
        messageId: ev.message_id,
        path: ev.sandbox_path,
      });
    }
  };

  while (true) {
    const { done, value } = await reader.read();
    if (done) break;
    buf += dec.decode(value, { stream: true });

    const lines = buf.split("\n");
    buf = lines.pop() ?? "";

    for (const line of lines) {
      if (!line.startsWith("data: ")) continue;
      const payload = line.slice(6).trim();
      if (payload === "[DONE]") {
        onUpdate?.(snapshot());
        return snapshot();
      }

      let chunk: Chunk;
      try {
        chunk = JSON.parse(payload);
      } catch {
        continue;
      }

      const delta = chunk.choices?.[0]?.delta?.content;
      if (delta) content += delta;
      if (chunk.sentinel) handleSentinel(chunk.sentinel);
      if (chunk.choices?.[0]?.finish_reason === "stop") {
        conversationId = chunk.conversation_id;
      }
      onUpdate?.(snapshot());
    }
  }

  return snapshot();
}
```

### 6.1 使用示例

```typescript
const result = await chatStream(
  "http://127.0.0.1:5005",
  "your-token",
  {
    model: "gpt-4o",
    messages: [{ role: "user", content: "生成两张不同风格的猫" }],
  },
  (partial) => {
    // 实时 UI：partial.content、partial.images
  },
);

// 最终图
console.log(result.images);

// 结构化业务 JSON（识图等）
try {
  const data = JSON.parse(result.content);
} catch {
  // 纯文本
}

// 下一轮带上
// { conversation_id: result.conversationId }
```

### 6.2 `base64_chunked` 拼接

对同一产物（以 `file_id` 或 `slot_index`+`kind` 为 key）：

1. 收集 `artifact_chunk` 的 `data`，按 `chunk_index` 排序拼接。
2. 收到 `artifact_done` 后 `atob` / `Buffer.from` 解码展示。

---

## 7. 非流式

```json
{ "stream": false, "artifact_delivery": "url" }
```

响应示例：

```json
{
  "choices": [{
    "message": { "role": "assistant", "content": "..." },
    "finish_reason": "stop"
  }],
  "conversation_id": "conv-xxx",
  "sentinel": [
    { "event": "artifact", "kind": "generated_image", "slot_index": 1, "url": "..." },
    { "event": "artifact_slot_final", "kind": "generated_image", "slot_index": 1 }
  ]
}
```

- 正文：`choices[0].message.content`
- 产物：遍历 `sentinel` 数组，处理逻辑与流式相同

---

## 8. 场景对照

| 场景 | 正文 `content` | `sentinel` |
|------|----------------|------------|
| 普通对话 | 流式文字 | 通常无 |
| DALL·E 生图 | 可能很短或为空 | `artifact` + `artifact_slot_final` |
| 上传识图 / GPSR JSON | 拼完后一整段 JSON | 一般**无**生图 `artifact` |
| Code Interpreter | 说明文字 | `sandbox_file` + `url` |

---

## 9. 代理 URL

服务端在 `artifact_delivery: "url"` 时会返回绝对 URL：

| 类型 | 路径 |
|------|------|
| 生图 | `GET /api/image/proxy?conv_id={conversation_id}&file_id={file_id}` |
| 沙箱文件 | `GET /api/pdf/proxy?conv_id={conversation_id}&msg_id={message_id}&sandbox_path={path}` |

代理依赖服务端内存中的 **session**（`conv_id` → 本轮 ChatGPT token）。须在收到 `conversation_id` 之后、且与发起聊天的**同一 server 进程**内请求。

- 流式过程中收到 `sentinel.url` 即可下载（服务端会在首次拿到 `conversation_id` 时注册 session）。
- 若仍报 `Session not found or expired`：确认 `conv_id` 与当轮 `stop` chunk 一致；未重启 server；或改用 `artifact_delivery: base64` 免代理。

---

## 10. 检查清单

- [ ] `stream: true`，按行解析 `data: `，以 `[DONE]` 结束
- [ ] 正文只拼 `delta.content`；图片/文件只看 `sentinel`
- [ ] 最终图：`latest_per_slot` 下维护 `Map<slot_index, SlotImage>`
- [ ] 监听 `artifact_slot_final` 标记图位完成
- [ ] 保存 `conversation_id`（`stop` chunk）
- [ ] 业务 JSON 在流结束后再 `JSON.parse(content)`
- [ ] 新客户端设置 `artifact_markdown: false`，避免正文混入 markdown 链接

---

## 11. 调试与抓包

### 日常 `go run ./cmd/server/`

- 控制台有 `[debug-sse]`、`[handoff]`、`[image-ws]`、`[artifact]` 等日志。
- **默认不会**把完整 SSE/WS 落盘（体积大）。

### 完整抓包（推荐排查生图提前结束）

```bash
go run ./cmd/stream-capture/ -config config.json -case image
```

输出目录：`testdata/stream-captures/<时间>-image/`（`sse.ndjson`、`ws.ndjson`、`chat_result.json`）。

### 生图「两张图待选后突然结束」

常见原因（已修复）：把用户上传的 **参考图** `file_id` 误当成生图结果，4s idle 提前关掉 WebSocket。

日志特征：

- `[artifact] 生图 file_id: [file_... file_...]` 与请求里 `referenced_image_ids` 相同
- `reply=0字`，`stream-deltas` 里出现 `referenced_image_ids` JSON 碎片
- 无 `[image-ws] 生图收齐` 行

修复后：仅带 `dalle.gen_id` 的 WS 更新才计入 idle；多图候选 idle 延长至 30s。

### 网页还在生图但 API 已返回

原因：首张图出来后 **4s 无新图** 就结束 WS，但 ChatGPT 常继续 `async-task` 修图/多轮（`add-messages` 等）。

修复后：

- 默认 idle **15s**（async 进行中 **25s**，多图候选 **30s**）
- 仅 `async-task-start` 增加 `pending`；`add-messages` **不**增加 pending（避免无 complete 时永久卡住）
- 有图且 **20s** 无新图、仍无 `complete` 时自动 `stale pending` 清除
- 服务端日志前缀：`[image-ws][evt]` / `[async]` / `[img]` / `[diag]`，`block=` 说明为何还不能结束

结束信号（按优先级）：

1. `set-conversation-async-status` 且 `conversation_async_status=4`（网页端生图完成时常有）
2. `async-task-complete` / `end` 等
3. 最后一**张新图**后 idle 15～25s（`add-messages` 重复推送不会刷新 idle）

注意：`turn [DONE]` 只表示 turn 正文流结束，**不等于**生图结束。

示例：

```text
[image-ws][async] set-conversation-async-status status=4
[image-ws] 生图收齐 2 槽（... convStatus=true）
[image-ws][diag] exit_ok ... block=ok
```

---

## 相关文档

- [API.md](../API.md) — 鉴权、模型、多模态请求
- [README.md](../README.md) — 部署与项目结构
