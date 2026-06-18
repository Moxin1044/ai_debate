<div align="center">

# AI 辩论系统

让两个 AI 模型就任意话题展开多轮辩论，并由独立裁判进行实时点评与最终总评的终端应用。

基于 Bubble Tea + Lip Gloss 打造的科技感 TUI 界面。

[快速开始](#快速开始) · [辩论模式](#辩论模式) · [配置说明](#配置说明) · [快捷键](#快捷键) · [发布版本](https://github.com/Moxin1044/ai_debate/releases)

</div>

---

## 功能特性

- 📋 **四种辩论模式** — 自由辩论、牛津式、林肯-道格拉斯式、政策辩论
- 🎯 **自定义立场** — 可手动设定正反方立场，或让 AI 自动生成
- 💬 **多轮辩论** — 支持自定义辩论轮次（默认 30 轮）
- 👨‍⚖️ **裁判系统** — 每轮结束后裁判点评，辩论结束后输出总评
- ✅ **事实核查** — 裁判核实数据与事实真实性，禁止伪造
- 🎭 **差异化口吻** — 正反方采用不同温度参数，避免风格雷同
- 📡 **实时流式输出** — 逐字显示，无需等待整段生成完成
- 📄 **导出报告** — 自动导出 PDF + Markdown，含论点图谱、关键分歧、裁判总评
- 🎨 **科技感 TUI** — 基于 Bubble Tea + Lip Gloss 的现代化终端界面
- 📜 **历史滚动浏览** — 内置视口组件，滚动查看历史内容
- 📱 **跨平台支持** — Windows、Linux、macOS（amd64 / arm64）

## 快速开始

### 方式一：下载预编译版本

前往 [Releases](https://github.com/Moxin1044/ai_debate/releases) 下载对应平台的可执行文件。

### 方式二：从源码构建

```bash
# 克隆仓库
git clone https://github.com/Moxin1044/ai_debate.git
cd ai_debate

# 安装依赖
go mod download

# 运行
go run main.go

# 或编译为可执行文件
go build -ldflags="-s -w" -o ai_debate .
./ai_debate
```

### 配置 API

首次运行时，程序会在当前目录自动生成 `config.yml` 默认配置（不含 API Key）。请填写 Key 后重新运行。

```yaml
api:
  key: "your-api-key"
  base_url: "https://api.siliconflow.cn/v1"
  model: "deepseek-ai/DeepSeek-V4-Pro"

debate:
  default_rounds: 30
  pro_temperature: 0.6    # 正方温度（偏理性）
  con_temperature: 1.0    # 反方温度（偏发散）
  judge_temperature: 0.3  # 裁判温度（偏严谨）
```

## 使用说明

1. **输入辩论主题** — 启动后输入主题（如「人工智能是否会取代人类创作」），按 Enter 确认
2. **选择辩论模式** — 使用 ↑↓ 键选择模式，按 Enter 确认
3. **输入辩论轮次** — （自由辩论模式）输入轮次，直接 Enter 使用默认 30 轮
4. **自定义立场（可选）** — 格式 `正方:xxx;反方:xxx`，留空则 AI 自动生成
5. **观看辩论** — 正方与反方按模式规则轮流发言，每轮结束后裁判点评
6. **滚动浏览** — 使用方向键或翻页键滚动查看历史内容
7. **导出报告** — 辩论结束后自动生成 PDF + Markdown 报告
8. **退出程序** — 按 `Esc` 或 `Ctrl+C` 退出

## 辩论模式

| 模式 | 说明 | 流程 |
|------|------|------|
| **自由辩论** | 双方自由交替发言，灵活多变 | 正方 → 反方 × N 轮 → 裁判总评 |
| **牛津式辩论** | 经典英式辩论赛制 | 开篇立论 → 交叉质询 × N → 结辩 |
| **林肯-道格拉斯式** | 美式一对一制 | 立论 → 质询 → 立论 → 质询 → 反驳 × N → 结辩 |
| **政策辩论** | 政策方案论证 | 政策方案 → 质询 → 替代方案 → 质询 → 反驳 × N → 结辩 |

## 快捷键

| 按键 | 功能 |
|------|------|
| `Enter` | 确认输入 / 开始辩论 |
| `↑` / `↓` | 逐行滚动 / 选择模式 / 选择立场 |
| `PgUp` / `PgDown` | 翻页滚动 |
| `Esc` / `Ctrl+C` | 退出程序 |

## 导出报告

辩论结束后自动生成以下文件（保存在当前目录）：

- **Markdown 报告**：`{主题}.md`
  - 双方立场
  - 论点图谱（正方/反方论点脉络）
  - 裁判逐轮点评
  - 关键分歧与裁判总评
  - 完整辩论记录
- **PDF 报告**：`{主题}.pdf`
  - 结构化排版，含大纲、裁判总评、完整记录

## 配置说明

| 参数 | 说明 | 默认值 |
|------|------|--------|
| `api.key` | API 密钥（必填） | 空 |
| `api.base_url` | API 基础 URL | `https://api.siliconflow.cn/v1` |
| `api.model` | 模型名称 | `deepseek-ai/DeepSeek-V4-Pro` |
| `debate.default_rounds` | 默认辩论轮次 | 30 |
| `debate.pro_temperature` | 正方温度（理性=低，创意=高） | 0.6 |
| `debate.con_temperature` | 反方温度 | 1.0 |
| `debate.judge_temperature` | 裁判温度（严谨=低） | 0.3 |

## 项目结构

```
ai_debate_py/
├── main.go              # 主程序：TUI 界面与辩论逻辑
├── config.yml           # 配置文件（自动生成）
├── go.mod               # Go 模块定义
├── go.sum               # 依赖校验
├── .github/
│   └── workflows/
│       └── release.yml  # GitHub Actions 自动构建发布
└── README.md
```

## 技术栈

| 技术 | 用途 |
|------|------|
| [Bubble Tea](https://github.com/charmbracelet/bubbletea) | 终端 UI 框架（Elm 架构） |
| [Lip Gloss](https://github.com/charmbracelet/lipgloss) | 终端样式与布局 |
| [Bubbles](https://github.com/charmbracelet/bubbles) | TUI 组件（viewport 滚动） |
| [go-yaml](https://github.com/gopkg.in/yaml.v3) | YAML 配置解析 |
| [gofpdf](https://github.com/jung-kurt/gofpdf) | PDF 报告生成 |
| OpenAI API | 兼容接口的大模型调用 |

## CI/CD

项目通过 GitHub Actions 实现自动构建发布：

- 推送 `V*` 格式的 tag（如 `V1.0`、`V2.1.3`）触发构建
- 自动构建 Windows、Linux、macOS（amd64 + arm64）四个平台的可执行文件
- 自动创建 GitHub Release 并上传产物，生成 Release Notes

```bash
git tag V1.0
git push origin V1.0
```