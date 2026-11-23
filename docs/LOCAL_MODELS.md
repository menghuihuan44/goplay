# 本地模型

GoPlay 的本地模式不是“把一个模型塞进安装包”，而是：

1. 首次启动探测设备（RAM、架构、芯片型号）；
2. 按档位推荐一套**角色分工**；
3. 只下载该档位需要的 GGUF；
4. 用外部 @llama-server@ 提供 OpenAI 兼容接口，核心通过 @/v1/chat/completions@ 调用。

模型和 llama.cpp 运行时都不属于安装包，用户可替换/删除，下载带大小与 SHA256 校验。

## 档位

| 档位 | 最低 RAM | 感知/过滤 | 策略推理 | 输出校验 | 目标 TTFT | 目标 TPS | 上下文 | 线程 |
|------|---------|-----------|----------|----------|-----------|----------|--------|------|
| low | 8 GiB | 规则 | Qwen2.5-0.5B-Instruct Q4_K_M | 规则 | 400ms | 25 | 2048 | 4 |
| medium | 16 GiB | SmolLM2-360M 混合 | Qwen2.5-1.5B-Instruct Q4_K_M | 规则 | 600ms | 15 | 4096 | 4 |
| high | 32 GiB | Qwen2.5-0.5B 混合 | Qwen2.5-3B-Instruct Q4_K_M | 规则 | 900ms | 8 | 8192 | 6 |

**分工而非叠加**：

- 感知/过滤：规则或极小模型把原始画面/状态压成少量候选；
- 策略推理：中等小模型只在候选动作中选，不负责感知和最终合法性；
- 输出校验：规则引擎做动作白名单、风险、坐标、速率检查，永远不交给模型。

这不是多个模型投票集成——每个角色解决不同子问题，最终动作由策略模型提议、规则引擎裁决。

## 模型 pin

| ID | 文件 | 大小 | License | 说明 |
|----|------|------|---------|------|
| @qwen2.5-0.5b@ | @qwen2.5-0.5b-instruct-q4_k_m.gguf@ | 491,400,032B | Apache-2.0 | low/medium/high 策略或感知 |
| @qwen2.5-1.5b@ | Q4_K_M | 1,117,320,736B | Apache-2.0 | medium 策略 |
| @qwen2.5-3b@ | Q4_K_M | 2,104,932,768B | Apache-2.0 | high 策略 |
| @smollm2-360m@ | Q8_0 | 386,404,992B | Apache-2.0 | medium 感知 |
| @gemma-3-270m@ | Q4_0 | 241,574,944B | Gemma Terms | 可选、gated |

SHA256 固定在 @internal/localmodel/catalog.go@；下载器拒绝目录外的 URL/未 pin 文件。Gemma 需要接受上游条款并自行取得访问权，因此默认不是必需项。

## 命令

```powershell
# 设备推荐（JSON）
go run ./cmd/goplay-model recommend
go run ./cmd/goplay-model recommend --tier high --ram-gb 16

# 下载（默认当前档位必需模型）
$env:GOPLAY_HF_ENDPOINT = "https://hf-mirror.com"
go run ./cmd/goplay-model download --tier low
go run ./cmd/goplay-model download --tier low --model qwen2.5-0.5b --force

# 启动 llama-server（前台，NDJSON 状态）
go run ./cmd/goplay-model serve --tier low --runtime "C:\path\to\llama-server.exe"

# 基准（对已运行的 OpenAI 兼容服务）
go run ./cmd/goplay-model bench --url http://127.0.0.1:8421/v1 --tier low --strict
```

运行时查找顺序：@--runtime@ → @GOPLAY_LLAMA_SERVER@ → @PATH@。llama-server 只允许 loopback host。

## daemon 集成

@internal/localruntime@ 在 daemon 启动时：

1. 探测硬件并 @Recommend@；
2. 选择策略模型文件；
3. 若缺失且 @local.auto_download=true@，调用下载器；
4. 启动 llama-server，等待 @/health@；
5. 构造 @llm.OpenAI{BaseURL: http://127.0.0.1:<port>/v1}@，交给 @llm.Router@。

模式行为：

- @-llm auto@：本地失败不致命，回退 API 或规则策略；
- @-llm local@：本地启动失败致命，避免悄悄降级；
- @-llm api@：不启动本地服务；
- @-llm offline@：只用启发式/Mock，适合测试。

@GET /api/local/status@ 返回档位、硬件、模型是否存在、服务 PID、provider/model、错误与 note。@POST /api/local/recommend@ 供 Android 首次连接前按设备 RAM 询问档位，避免手机端内置模型。

## 配置示例

```json
{
  "addr": "127.0.0.1:8420",
  "llm": { "mode": "local" },
  "local": {
    "models_dir": "C:\\Users\\me\\AppData\\Local\\GoPlay\\models",
    "runtime_path": "C:\\tools\\llama.cpp\\llama-server.exe",
    "server_port": 8421,
    "auto_download": true,
    "preferred_tier": "low",
    "context_tokens": 2048,
    "threads": 4
  }
}
```

## 实测与限制

本机（Windows，15.63GiB RAM，判定 low 档）：

- 下载 491,400,032B，SHA256 通过，34.8 秒（hf-mirror）；
- 冷启动 TTFT 3458ms，超出 400ms 目标；预热后 48.4ms、63.9 tok/s；
- 真实 daemon 决策：79 动作、1 局胜利、7 次 LLM 调用、0 失败、0 回退。

注意事项：

- 冷启动包含模型加载，daemon 应尽量在启动时预热；
- Windows 上 @Stop@ 使用 Kill，可能不优雅，后续可改为先发关闭请求；
- bench 的 token 计数依赖服务端 @stream_options.include_usage@；
- 芯片型号探测是尽力而为，RAM 是主判据；
- 小模型会回显注释/多余文本，策略层用 @groundLLMAction@ 把输出映射到合法动作，映射失败才回退。
