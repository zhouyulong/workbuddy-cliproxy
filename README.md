# workbuddy-cliproxy

把**腾讯 CodeBuddy**（`copilot.tencent.com`）封装成 [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI)(CPA)插件,任何支持 OpenAI / Anthropic 协议的客户端(Claude Code、Cursor、Cline、SDK……)都能直接调用 CodeBuddy 背后的模型。

对 [Sliverkiss/cpa-plugin](https://github.com/Sliverkiss/cpa-plugin) 公开 `workbuddy.so` 的 clean-room 逆向重写,补齐了源码与 x86_64 支持;workbuddy 的原始设计归属 Sliverkiss。

## 工作原理

在 CPA 里注册为 `workbuddy` provider:负责 CodeBuddy 扫码登录、token 刷新,并把请求转发到 `copilot.tencent.com/v2/chat/completions`。登录后凭据存为 `workbuddy.json`。

## 模型

登录后，插件使用账号的认证 token 请求官方客户端使用的 `https://copilot.tencent.com/v3/config`，自动注册 `data.models` 中的全部聊天模型，新增模型无需修改源码。模型 ID 自动去重，并读取上游的输入、输出 token 限制。目录中的图像和视频生成模型不注册到当前仅支持聊天补全的执行器；具体可用性以账号权限为准。`copilot.tencent.com/v1/models` 当前返回 404，不能作为该服务的发现接口。

登录前使用内置的 10 个模型完成静态注册。CPA 在启动、重新加载凭据等触发账号模型查询时，会重新获取上游列表。模型查询总等待时间最多 2 秒；查询失败或返回空列表时，使用该凭据上次成功获取的模型列表，没有缓存时使用内置模型，并记录警告，避免查询故障导致账号无法调用模型。

## 安装

**前置**:带 CGO / 动态插件支持的 CLIProxyAPI、CodeBuddy 账号、Go 1.26+ 与 C 编译器；编译架构需与 CPA 实例一致(amd64 / arm64)。插件仍基于 v7.2.30 SDK，另以本机 CPA 核心 v8.0.48 完成隔离 ABI/转发测试；没有升级或替换宿主。

```bash
git clone https://github.com/zhouyulong/workbuddy-cliproxy.git
cd workbuddy-cliproxy
CGO_ENABLED=1 GOOS=linux GOARCH=amd64 \
  go build -buildmode=c-shared -o workbuddy.so .
```

产物:`.so`(Linux)/ `.dylib`(macOS)/ `.dll`(Windows)。放到 CPA 的 `plugins/` 目录,在 `config.yaml` 启用:

```yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    workbuddy: { enabled: true, priority: 100 }
```

重启 CPA,日志出现 `plugin loaded ... plugin_id=workbuddy` 即成功,`GET /v1/models` 可查看已注册的模型。然后到 CPA 面板添加 workbuddy 凭据,扫码登录 CodeBuddy，插件将获取该账号的完整模型列表。

## 使用

CPA 默认端口 `8317`,API key 见 `config.yaml` 的 `api-keys`。

| 协议 | Base URL |
|------|----------|
| OpenAI | `http://<host>:8317/v1` |
| Anthropic | `http://<host>:8317`(不带 `/v1`,走 `x-api-key`) |

```bash
# Claude Code
export ANTHROPIC_BASE_URL=http://localhost:8317
export ANTHROPIC_API_KEY=<api-key>
export ANTHROPIC_MODEL=hy3-preview-agent
claude
```

```bash
# curl / OpenAI
curl http://localhost:8317/v1/chat/completions \
  -H "Authorization: Bearer <api-key>" -H "Content-Type: application/json" \
  -d '{"model":"hy3-preview-agent","messages":[{"role":"user","content":"你好"}],"stream":true}'
```

流式 / 非流式都支持;非流式请求会被内部转成流式再聚合(CodeBuddy 上游 `code 11101` 拒绝非流式)。

## Claude Code 兼容性

腾讯 CodeBuddy 的内容审核把 Claude Code 的两句固定 system 模板逐字加进了黑名单,命中即回"敏感内容"拒答:

- `You are Claude Code, Anthropic's official CLI for Claude.`(身份句)
- `Main branch (you will usually use this for PRs)`(git 注入句)

任何一字改动都绕过(精确匹配,非语义审核)。workbuddy 转发前会自动把这两句做最小改写(`CLI`→`CLI tool`、`Main branch`→`Default branch`),语义不变,Claude Code 照常工作。

属于 cat-and-mouse:腾讯哪天多加模板句,得跟着改 `sanitizeBlockedTemplates`。

## 思考模式

插件向 CPA 同时声明模型的 `Thinking.Levels` 与 `reasoning_effort` 支持，避免已发现模型因缺少能力声明而被宿主删除推理参数。静态兜底列表中的已知模型也使用同一份能力声明。账号的模型发现、分页、超时与失败兜底逻辑仍然保留。

已知模型以本渠道 2026-11-09 的能力表为准（不是各模型厂商直连 API 的通用规则）：

| 模型 | 可用 `reasoning_effort` | 未指定时 |
|---|---|---|
| Space-Bunny、MiniMax-M3.1-Flash | `low / medium / high / xhigh / max` | `max` |
| Step-5-Preview | `low / medium / high` | `high` |
| Kimi-K3 | `low / high / xhigh` | `high` |
| GLM-5.3、GLM-5.3-Flash、Kimi-K2.8-Preview、Kimi-K3.1-Pro、DeepSeek-V4.1-Flash | `low / high / max` | `high` |
| GLM-5.2、DeepSeek-V4-Flash、DeepSeek-V4-Pro | `high / xhigh` | `high` |
| Hy3 | `low / high` | `high` |
| Hy4 preview、Hy4、Xiaomi MiMo-V2.6-Pro | `high` | `high` |

- 有效的显式档位原样转发，不再把 Hy3 的 `low` 强制改为 `high`。`ultra` 不是这个渠道的档位，不映射为 `max`。不支持的显式档位会由 CPA 或插件拒绝；跨协议转换时，CPA 可能先把档位映射为目标模型支持的值，因此应以实际出站参数为准，而非只看客户端或使用记录里的选项。
- 发送推理档位时，参照原生 WorkBuddy 客户端补充 `reasoning_summary: "auto"`，保留显式的摘要设置（包括 `none`）。这是请求可见摘要，不是模型实际进行内部推理的证明。不会额外插入面向直连 DeepSeek API 的 `thinking.type`。
- 旧的 `hy3-preview` / `hy3-preview-agent` 保留历史 `high` 默认值，但不再无条件覆盖显式选择；新表未给出其完整档位范围，因此优先采用上游声明的能力，没有声明时使用兼容透传。
- 动态发现的新模型若提供 `reasoning.supportedEfforts`，读取该能力；若能力未知或不完整，使用 CPA 兼容透传路径，不猜测它支持的档位与默认值。上游明确声明不支持推理的模型仍按非推理模型注册。
- 思考内容通过 SSE 的 `delta.reasoning_content` 保留，非流式聚合也保留该字段和上游用量。客户端是否显示摘要仍取决于它自己的能力声明与渲染方式。

### 上下文、输出与积分信息

`model_profiles.go` 分开记录输入/输出硬上限、可选上下文窗口、默认窗口和积分倍率。CPA SDK 模型信息能表达硬上限，但不能表达多档窗口选择、默认窗口或本渠道积分倍率：后几项只作为参考元数据保留，**不会自动配置客户端界面，也不参与计费或自动截断**。

例如 Space-Bunny 默认窗口为 1M；DeepSeek-V4-Flash 与 Step-5-Preview 的窗口只有 300K/1M；Hy4 系列输入上限为 960K。GLM-5.3-Flash 的“131K”采用原生目录中的精确输出上限 131072。Hy3 未提供窗口、输出与倍率，继续采用上游元数据，不填造数值。真实模型 ID 保持不变；模型别名仅用于匹配能力，不改写上游 ID。

自定义客户端模型目录如果仍统一提供 `low ... ultra`、默认 `medium`，不会因替换插件而自动修正；部署后还应单独核对客户端目录。不要为了消除错误而给不支持的模型静默降档。

### 离线回归验证

```bash
go test -count=1 ./...
go vet ./...
python3 tests/offline_host_smoke.py \
  --cpa-core /absolute/path/to/cli-proxy-api \
  --output /absolute/path/to/test-results
```

集成脚本要求 Go、CGO 工具链和已缓存的模块依赖，使用 CPA v8 配置格式。它复制源码并临时加入内存 HTTP 模拟层，使用假凭据和独立本机端口，验证真实宿主的 Chat Completions/Responses、流式/非流式、各档位与默认值、错误档位及摘要事件。结果与宿主日志保存在指定输出目录；退出时停止测试实例，不触碰现有服务或安装目录。

**集成脚本的临时库含模拟响应，绝不能部署。** 实际插件应从未经诊断注入的仓库源码单独构建。离线测试能证明参数转发与模拟推理事件转换，不能证明真实上游一定返回摘要、消耗多少推理 token，或桌面端一定显示“思考中”。

## 流式

真流式(async):转发上游时边读边通过 `host.stream.emit` 把每个 chunk 实时推给 CPA,客户端逐字收到(不是等收齐了一股脑)。hy3 几千字的思考过程也是实时流出的,不是憋半天再刷出来。

## License

MIT。
