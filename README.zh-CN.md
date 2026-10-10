# nicedroid

**把 Factory `fk-` API Key 变成标准的 OpenAI / Anthropic / Responses 接口。**

nicedroid 是一个单文件 Go 网关，架在 Factory 的 LLM 网关（Droid CLI 用的同一套）
前面。把你现有的 OpenAI 或 Anthropic SDK 指过来，就能用客户端本来就支持的那套协议
调用 Droid 的模型，包括便宜的开源 **Droid Core** 池。内置管理后台，可以做配额监控、
Key 池管理、供应商路由和逐请求成本核算。

> [!IMPORTANT]
> **仅供学习和参考。** 本项目是对内部未公开接口的逆向工程实现，用于记录网关的工作方式，
> 不适用于生产环境，请勿转售，也不能替代官方产品。与 Factory 无关联，未获其认可或支持。

[English README →](README.md)

---

## 功能

- **四种协议自由互通。** OpenAI Chat Completions、Anthropic Messages、OpenAI
  Responses、Google generate。任意入口都能请求任意模型：Anthropic 客户端要 GLM，
  代理会自动转换请求体和流式响应。
- **Droid Core 池可用。** 开源模型池（`glm-5.3-flash`、`kimi-k3`、
  `deepseek-v4.1-flash` 等）除了常规协议，也能通过 `/v1/responses` 调用。
- **多供应商路由。** 同一个模型由多家后端承载（fireworks、baseten、databricks、
  mistral）。可以手动固定最快的供应商，也可以用内置的流式测速让它自动选。
  上游返回 402 / 429 / 5xx 时自动切换下一个供应商，客户端无感知。
- **配额感知。** 轮询 Factory 的 billing limits，展示 standard 与 core 两个池在
  5 小时 / 周 / 月三个窗口下的水位。
- **如实计量。** 逐请求记录延迟、首字延迟（TTFT）、缓存读/写 token、TPS，以及按
  CLI 同款公式估算的 Factory Standard Credits。
- **单文件部署。** React 管理界面用 `go:embed` 打包，状态存一个 SQLite 文件，
  不依赖任何外部服务。

## 接口

| 方法   | 路径                        | 协议                      |
| ------ | --------------------------- | ------------------------- |
| `POST` | `/v1/chat/completions`      | OpenAI Chat Completions   |
| `POST` | `/v1/messages`              | Anthropic Messages        |
| `POST` | `/v1/messages/count_tokens` | Anthropic token 计数      |
| `POST` | `/v1/responses`             | OpenAI Responses          |
| `POST` | `/v1/generate`              | Google generate（透传）   |
| `GET`  | `/v1/models`                | 模型列表                  |
| `GET`  | `/healthz`                  | 健康检查                  |

## 快速开始

需要 Go 1.27+，Node 20+（Node 只在重新构建前端时需要；仓库里已提交构建好的
`web/dist`，所以干净克隆后直接 `go build` 也能过）。

```bash
git clone https://github.com/Sibuxiangx/nicedroid
cd nicedroid
./build.sh
```

启动：

```bash
NICEDROID_ADMIN_PASSWORD=change-me ./nicedroid
```

打开 <http://localhost:3000> 登录，在「Key 管理」页添加你的 Factory API Key
（`fk-` 开头）。这个 Key 就是网关向上游出示的身份。

想自己构建也可以：

```bash
cd web && npm install && npm run build && cd ..
go build -o nicedroid ./cmd/server
```

## 使用

配置好上游 Key 后，下游客户端用管理后台创建的客户端 Key
（`sk-nicedroid-…`）鉴权。客户端 Key 是可选的：一个都不建时 `/v1/*` 完全开放。

**OpenAI 兼容：**

```bash
curl http://localhost:3000/v1/chat/completions \
  -H 'content-type: application/json' \
  -H 'authorization: Bearer sk-nicedroid-…' \
  -d '{
    "model": "glm-5.3-flash",
    "messages": [{"role": "user", "content": "你好"}],
    "stream": true
  }'
```

**Anthropic 兼容** —— 把 `ANTHROPIC_BASE_URL` 指向本服务，Claude Code 和任意
Anthropic SDK 都能直接用：

```bash
curl http://localhost:3000/v1/messages \
  -H 'content-type: application/json' \
  -d '{
    "model": "claude-sonnet-5-5",
    "max_tokens": 256,
    "messages": [{"role": "user", "content": "你好"}]
  }'
```

**OpenAI Responses：**

```bash
curl http://localhost:3000/v1/responses \
  -H 'content-type: application/json' \
  -d '{"model": "deepseek-v4.1-flash", "input": "你好"}'
```

## 模型

模型目录不在这个仓库里手工维护。[`manifest.json`](manifest.json) 是唯一事实来源
（写作时为 93 个模型），内容从 Droid CLI 自己的模型注册表提取，Go 二进制在构建时嵌入该文件。

每个条目带上路由和计费所需的全部信息：

```json
{
  "id": "glm-5.3-flash",
  "name": "GLM-5.3-Flash",
  "provider": "factory",
  "providers": ["fireworks", "baseten", "databricks"],
  "quota_pool": "core",
  "vision": true,
  "reasoning": { "efforts": ["low", "high", "max"], "default": "high" },
  "cost": {
    "token_multiplier": 0.06,
    "output_multiplier": 3.34,
    "cache_read_multiplier": 0.2
  }
}
```

`provider` 是承载该模型的平台，`providers` 是网关可以派发的后端。开源模型两者不同：
它们属于 Factory，但由 fireworks、baseten、databricks 或 mistral 实际承载。
云市场传输层（`azure_openai`、`bedrock_anthropic` 等）无法用 `x-api-provider` 选择，
提取时会被过滤掉。

配额分两个池：**standard**（Claude、GPT、Gemini、Grok）与 **core**（便宜的开源模型，
其中多个由不止一家后端承载）。Gemini 仅支持透传：只能通过 `/v1/generate` 调用，
不参与协议转换。

管理后台按平台分区展示模型，并把 CLI 已下线的模型折叠起来——写作时为 59 个在用、
34 个历史。注册表给出弃用日期、或已从 CLI 自己的选择器中隐藏的，都算历史模型；
后台会同时显示弃用日期和建议的替代模型。`GET /v1/models` 也会列出历史模型，
但带上 `deprecated`、`retired_on`、`replacement` 标记，方便已有客户端自行过滤。

### 目录如何保持最新

目录本身打包在 Droid CLI 里，所以 `manifest.json` 跟踪的就是 CLI 的版本——
`cli.version` 字段记录了它来自哪个构建，代理也用它向上游表明身份。
[刷新 workflow](.github/workflows/update-models.yml) 每天把它和 npm 上最新的
`@factory/cli` 比对。两者不一致时，它会下载对应的构建产物、校验 SHA-256、
确认该构建自报的版本与发布版本一致，然后重新提取注册表并提交。

提取是确定性的：同一个二进制永远产出逐字节相同的 JSON，不含时间戳等随运行变化的字段。
正因如此，比对版本就足够了——版本没变就不可能产生 diff，每天的定时任务是无操作，
而不是一次空提交。

想手动做同样的事：

```bash
go run ./tools/extract-manifest ~/.local/bin/droid \
  --version "$(droid --version)" \
  --out manifest.json
```

提取器会自我校验：如果 manifest 缺少 CLI 自己在 `droid exec --help` 里列出的模型，
它会拒绝写出文件。所以 bundle 结构变化会让任务失败，而不是悄悄让目录缩水。

## 供应商选择

Factory 网关根据 `x-api-provider` 请求头决定由哪家后端承载。后端之间速度差异明显：
实测 `glm-5.3-flash`，databricks 的端到端耗时比 fireworks 和 baseten 快约 20%。
因此 nicedroid 会逐请求解析供应商：

- **手动固定**：模型目录页的供应商下拉框，选择存在 SQLite 里，清空即恢复目录默认。
- **测速选择**：点「测速」按钮，服务端对每个供应商跑同一段流式生成，轮流进行 1–3 轮，
  每轮结果实时推给浏览器，按各轮中位数排名。勾选「自动固定最快供应商」即可一键应用。
- **自动回退**：运行时先试已固定的供应商，再按目录顺序试其余；遇到 402 / 429 / 5xx
  自动切下一个。

测速报告的是**端到端吞吐**（输出 token ÷ 请求总耗时），不是「输出 token ÷ 生成窗口」。
上游把整段响应缓冲后一次性下发时，生成窗口会被压到几毫秒，那个比值能虚高到几十万
（实测出现过 299953 tok/s）。总耗时无法这样伪造。首字延迟单独展示。

## 管理后台

与网关同一个二进制，路径 `/admin`：

- **Dashboard** —— 两个配额池的水位、请求量、缓存命中率、延迟与 TPS 趋势。
- **Key 管理** —— 上游 Key 池（加权轮询、跳过耗尽、一键校验）与下游客户端 Key。
- **模型目录** —— 完整目录、推理档位、供应商固定与流式测速。
- **请求日志** —— 每条请求的模型、供应商、配额池、状态、延迟、TTFT、token 数、
  缓存读取、TPS 与估算积分。
- **设置** —— 系统提示词覆盖，以及用于冒烟测试的 Playground。

首次启动会生成管理员密码并打印到日志，除非设置了 `NICEDROID_ADMIN_PASSWORD`。

## 配置

全部通过环境变量配置。

| 变量                                | 默认值                   | 说明                     |
| ----------------------------------- | ------------------------ | ------------------------ |
| `NICEDROID_LISTEN`                  | `:3000`                  | 监听地址                 |
| `NICEDROID_DB`                      | `nicedroid.db`           | SQLite 路径              |
| `NICEDROID_UPSTREAM`                | `https://api.factory.ai` | 上游基础地址             |
| `NICEDROID_SYSTEM_PROMPT`           | Droid 门禁提示词         | 首条 system 消息（见下） |
| `NICEDROID_ADMIN_USER`              | `admin`                  | 管理员用户名             |
| `NICEDROID_ADMIN_PASSWORD`          | 首次启动随机生成         | 管理员密码               |
| `NICEDROID_JWT_SECRET`              | 随机生成                 | 会话签名密钥             |
| `NICEDROID_QUOTA_POLL_SECONDS`      | `60`                     | 配额轮询间隔             |
| `NICEDROID_REQUEST_TIMEOUT_SECONDS` | `600`                    | 上游请求超时             |
| `NICEDROID_LOG_RETENTION_DAYS`      | `30`                     | 请求日志保留天数         |

## 工作原理

两条约束决定了整个代理的形态：

1. **内容门禁。** 上游网关会拒绝任何不以
   `You are Droid, an AI software engineering agent built by Factory.`
   这条 system 消息开头的 LLM 请求，返回 `403`。nicedroid 会自动注入，并把你自己的
   system 提示词追加在它后面。
2. **版本识别。** 请求必须携带 `factory-cli` 的 user agent 和客户端版本号，代理因此
   以和 CLI 相同的方式表明身份。

满足这两点后，代理可以在四个协议之间双向自由转换，包括流式（SSE）与 tool call。

## 注意事项

- **与 Factory 无关联。** 这是个非官方客户端，走的是内部接口。这些接口没有文档，
  随时可能变；CLI 的一次升级就可能让本项目失效。
- 调用仍然消耗你自己的 Factory 配额，由 Factory 计费。日志里的积分是按 CLI 同款公式
  推算的估值，不是权威数字。
- 设计上**没有模型降级**：配额池耗尽时，上游错误原样返回给调用方。
- Gemini 模型仅支持透传，只能通过 `/v1/generate` 调用，不参与协议转换。
- 数据库文件请自行保管好，上游 API Key 在其中以明文存储。

## 开发

架构、上游约束和产品策略的取舍见 [AGENTS.md](AGENTS.md)。简述：`./build.sh` 构建全部；
项目没有测试套件，验证方式是启动服务用 `curl` 打接口。

## 许可与用途

不授予任何许可证。代码公开**仅供学习和参考**：它记录了网关的协议格式、模型目录与
积分模型的实现方式，供自建类似工具的人研究。

本项目是针对内部未公开接口的逆向工程，与 Factory 无关联，也未获其认可或支持。
请勿转售、对外提供服务，或在生产环境依赖它。保留所有权利。
