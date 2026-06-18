<div align="center">

# AI 辩论系统

让两个 AI 模型就任意话题展开多轮辩论，并由独立裁判进行实时点评与最终总评的终端应用。

基于 Bubble Tea + Lip Gloss 打造的科技感 TUI 界面。

[快速开始](#快速开始) · [配置说明](#配置说明) · [快捷键](#快捷键) · [发布版本](https://github.com/your-repo/releases)

</div>

---

## 功能特性

- 🎯 **自动立场确立** — AI 根据辩论主题自动分析并确立正方与反方立场，无需人工指定
- 💬 **多轮辩论** — 支持自定义辩论轮次（默认 30 轮），双方轮流发言
- 👨‍⚖️ **裁判系统** — 每轮结束后裁判进行点评，所有轮次结束后输出总评
- ✅ **事实核查** — 裁判核实双方引用的数据和事实，禁止伪造与捏造
- 🎭 **差异化口吻** — 正反方采用不同的温度参数，避免雷同的论证风格
- 📡 **实时流式输出** — 逐字显示，无需等待整段生成完成
- 🎨 **科技感 TUI** — 基于 Bubble Tea + Lip Gloss 的现代化终端界面
- � **历史滚动浏览** — 内置视口组件，可自由滚动查看历史内容
- �📱 **跨平台支持** — Windows、Linux、macOS（amd64 / arm64）

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
2. **输入辩论轮次** — 输入轮次（直接 Enter 使用默认 30 轮）
3. **观看辩论** — 正方与反方轮流发言，每轮结束后裁判点评
4. **滚动浏览** — 使用方向键或翻页键滚动查看历史内容
5. **退出程序** — 按 `Esc` 或 `Ctrl+C` 退出

## 快捷键

| 按键 | 功能 |
|------|------|
| `Enter` | 确认输入 / 开始辩论 |
| `↑` / `↓` | 逐行滚动 |
| `PgUp` / `PgDown` | 翻页滚动 |
| `Esc` / `Ctrl+C` | 退出程序 |

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

## 许可证

[MIT License](LICENSE)
