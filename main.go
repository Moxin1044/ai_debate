package main

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	openai "github.com/sashabaranov/go-openai"
	"gopkg.in/yaml.v3"
)

// ─── 配置文件 ────────────────────────────────────────

type Config struct {
	API    APIConfig    `yaml:"api"`
	Debate DebateConfig `yaml:"debate"`
}

type APIConfig struct {
	Key     string `yaml:"key"`
	BaseURL string `yaml:"base_url"`
	Model   string `yaml:"model"`
}

type DebateConfig struct {
	DefaultRounds    int     `yaml:"default_rounds"`
	ProTemperature   float32 `yaml:"pro_temperature"`
	ConTemperature   float32 `yaml:"con_temperature"`
	JudgeTemperature float32 `yaml:"judge_temperature"`
}

func loadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取配置文件失败: %w", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("解析配置文件失败: %w", err)
	}
	return &cfg, nil
}

// ─── 样式 ────────────────────────────────────────────

var (
	accentColor  = lipgloss.Color("#00FFD1")
	warnColor    = lipgloss.Color("#FF6B6B")
	proColor     = lipgloss.Color("#7DF9FF")
	conColor     = lipgloss.Color("#FF6EC7")
	judgeColor   = lipgloss.Color("#FFD700")
	thinkColor   = lipgloss.Color("#888888")
	dimColor     = lipgloss.Color("#555555")
	titleColor   = lipgloss.Color("#00FFD1")
	dividerColor = lipgloss.Color("#333366")

	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(titleColor)

	proLabelStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#000000")).
			Background(proColor).
			Padding(0, 1)

	conLabelStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#000000")).
			Background(conColor).
			Padding(0, 1)

	judgeLabelStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#000000")).
			Background(judgeColor).
			Padding(0, 1)

	thinkLabelStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(thinkColor).
			Padding(0, 1)

	roundStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(accentColor)

	statusStyle = lipgloss.NewStyle().
			Foreground(dimColor)

	dividerStyle = lipgloss.NewStyle().
			Foreground(dividerColor)

	inputStyle = lipgloss.NewStyle().
			Foreground(accentColor).
			Bold(true)

	errorStyle = lipgloss.NewStyle().
			Foreground(warnColor).
			Bold(true)

	stanceTitleStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(accentColor)
)

// ─── 消息类型 ────────────────────────────────────────

type phase int

const (
	phaseInputTopic phase = iota
	phaseInputRounds
	phaseEstablishing
	phaseDebating
	phaseJudgeRound
	phaseJudgeFinal
	phaseDone
)

type streamChunkMsg struct {
	speaker  string
	chunk    string
	thinking bool
}

type streamDoneMsg struct {
	speaker  string
	fullText string
	thinking string
}

type streamErrMsg struct {
	err error
}

type positionsResultMsg struct {
	proStance string
	conStance string
	err       error
}

type judgeResultMsg struct {
	text     string
	thinking string
	err      error
}

type judgeFinalResultMsg struct {
	text     string
	thinking string
	err      error
}

type debateEntry struct {
	speaker  string
	text     string
	thinking string
}

// ─── 流式读取器 ──────────────────────────────────────

type streamReader struct {
	mu       sync.Mutex
	chunks   []streamChunkMsg
	done     bool
	doneMsg  streamDoneMsg
	err      error
}

func (r *streamReader) addChunk(msg streamChunkMsg) {
	r.mu.Lock()
	r.chunks = append(r.chunks, msg)
	r.mu.Unlock()
}

func (r *streamReader) setDone(msg streamDoneMsg) {
	r.mu.Lock()
	r.done = true
	r.doneMsg = msg
	r.mu.Unlock()
}

func (r *streamReader) setError(err error) {
	r.mu.Lock()
	r.err = err
	r.mu.Unlock()
}

func (r *streamReader) drain() ([]streamChunkMsg, bool, streamDoneMsg, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	chunks := r.chunks
	r.chunks = nil
	return chunks, r.done, r.doneMsg, r.err
}

// ─── TUI 模型 ────────────────────────────────────────

type model struct {
	phase  phase
	topic  string
	rounds int
	round  int

	proStance string
	conStance string

	input string

	entries        []debateEntry
	currentSpeaker string
	currentText    string
	currentThink   string

	roundProText string
	roundConText string

	client     *openai.Client
	proHistory []openai.ChatCompletionMessage
	conHistory []openai.ChatCompletionMessage
	proSystem  string
	conSystem  string

	cfg   *Config
	err   error
	width int
	height int

	viewport viewport.Model
	ready   bool

	reader *streamReader
	textWidth int
}

func initialModel(cfg *Config) model {
	config := openai.DefaultConfig(cfg.API.Key)
	config.BaseURL = cfg.API.BaseURL
	return model{
		phase:     phaseInputTopic,
		rounds:    cfg.Debate.DefaultRounds,
		client:    openai.NewClientWithConfig(config),
		cfg:       cfg,
		width:     80,
		height:    24,
		textWidth: 72,
	}
}

func (m model) Init() tea.Cmd {
	return nil
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		if m.width < 40 {
			m.width = 40
		}
		if m.height < 10 {
			m.height = 10
		}
		m.textWidth = m.width - 6
		if m.textWidth < 20 {
			m.textWidth = 20
		}

		if !m.ready {
			m.viewport = viewport.New(m.width, m.height-3)
			m.viewport.Style = lipgloss.NewStyle()
			m.ready = true
		} else {
			m.viewport.Width = m.width
			m.viewport.Height = m.height - 3
		}
		m.syncViewport()
		return m, nil

	case tea.KeyMsg:
		// 输入阶段处理
		if m.phase == phaseInputTopic || m.phase == phaseInputRounds {
			switch msg.Type {
			case tea.KeyCtrlC:
				return m, tea.Quit
			case tea.KeyEnter:
				if m.phase == phaseInputTopic {
					m.topic = strings.TrimSpace(m.input)
					if m.topic == "" {
						return m, nil
					}
					m.input = ""
					m.phase = phaseInputRounds
					return m, nil
				}
				input := strings.TrimSpace(m.input)
				if input == "" {
					m.rounds = m.cfg.Debate.DefaultRounds
				} else {
					n, err := strconv.Atoi(input)
					if err != nil || n <= 0 {
						m.err = fmt.Errorf("请输入有效的正整数")
						return m, nil
					}
					m.rounds = n
				}
				m.input = ""
				m.phase = phaseEstablishing
				return m, establishPositionsCmd(m.client, m.cfg.API.Model, m.topic)
			case tea.KeyBackspace:
				if len(m.input) > 0 {
					m.input = m.input[:len(m.input)-1]
				}
			default:
				m.input += msg.String()
			}
			return m, nil
		}

		// 辩论阶段：方向键滚动
		switch msg.Type {
		case tea.KeyCtrlC, tea.KeyEsc:
			return m, tea.Quit
		default:
			var cmd tea.Cmd
			m.viewport, cmd = m.viewport.Update(msg)
			return m, cmd
		}

	case positionsResultMsg:
		if msg.err != nil {
			m.err = msg.err
			m.phase = phaseInputTopic
			return m, nil
		}
		m.proStance = msg.proStance
		m.conStance = msg.conStance
		m.proSystem = buildProSystem(m.topic, m.proStance)
		m.conSystem = buildConSystem(m.topic, m.conStance)
		m.phase = phaseDebating
		m.round = 1
		m.proHistory = []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleUser, Content: fmt.Sprintf("请阐述你对「%s」的观点，给出你的核心论据。", m.topic)},
		}
		cmd := m.startStream("pro", m.proSystem, m.proHistory, m.cfg.Debate.ProTemperature)
		return m, cmd

	case streamDoneMsg:
		fullText := stripMarkdown(msg.fullText)
		thinking := stripMarkdown(msg.thinking)
		m.entries = append(m.entries, debateEntry{speaker: msg.speaker, text: fullText, thinking: thinking})
		m.currentText = ""
		m.currentThink = ""
		m.currentSpeaker = ""
		m.reader = nil

		if msg.speaker == "pro" {
			m.roundProText = msg.fullText
			m.proHistory = append(m.proHistory, openai.ChatCompletionMessage{
				Role: openai.ChatMessageRoleAssistant, Content: msg.fullText,
			})
			m.conHistory = append(m.conHistory, openai.ChatCompletionMessage{
				Role: openai.ChatMessageRoleUser,
				Content: fmt.Sprintf("对方观点：%s\n\n请反驳对方，并给出你的论据。", msg.fullText),
			})
			cmd := m.startStream("con", m.conSystem, m.conHistory, m.cfg.Debate.ConTemperature)
			m.syncViewport()
			return m, cmd
		}

		m.roundConText = msg.fullText
		m.conHistory = append(m.conHistory, openai.ChatCompletionMessage{
			Role: openai.ChatMessageRoleAssistant, Content: msg.fullText,
		})
		m.proHistory = append(m.proHistory, openai.ChatCompletionMessage{
			Role: openai.ChatMessageRoleUser,
			Content: fmt.Sprintf("对方观点：%s\n\n请反驳对方，并给出你的论据。", msg.fullText),
		})
		m.phase = phaseJudgeRound
		m.syncViewport()
		return m, judgeRoundCmd(m.client, m.cfg.API.Model, m.topic, m.round, m.rounds, m.roundProText, m.roundConText, m.cfg.Debate.JudgeTemperature)

	case judgeResultMsg:
		if msg.err != nil {
			m.err = msg.err
		} else {
			m.entries = append(m.entries, debateEntry{
				speaker:  "judge",
				text:     stripMarkdown(msg.text),
				thinking: stripMarkdown(msg.thinking),
			})
		}
		if m.round >= m.rounds {
			m.phase = phaseJudgeFinal
			m.syncViewport()
			return m, judgeFinalCmd(m.client, m.cfg.API.Model, m.topic, m.rounds, m.entries, m.cfg.Debate.JudgeTemperature)
		}
		m.round++
		m.phase = phaseDebating
		cmd := m.startStream("pro", m.proSystem, m.proHistory, m.cfg.Debate.ProTemperature)
		m.syncViewport()
		return m, cmd

	case judgeFinalResultMsg:
		if msg.err != nil {
			m.err = msg.err
		} else {
			m.entries = append(m.entries, debateEntry{
				speaker:  "judge_final",
				text:     stripMarkdown(msg.text),
				thinking: stripMarkdown(msg.thinking),
			})
		}
		m.phase = phaseDone
		m.syncViewport()
		return m, nil

	case streamErrMsg:
		m.err = msg.err
		return m, nil

	case pollMsg:
		if m.reader == nil {
			return m, nil
		}
		chunks, done, doneMsg, err := m.reader.drain()
		if err != nil {
			m.err = err
			return m, nil
		}
		// 处理累积的chunks
		for _, c := range chunks {
			if c.thinking {
				m.currentThink += c.chunk
			} else {
				m.currentText += c.chunk
			}
		}
		if done {
			m.syncViewport()
			return m, func() tea.Msg { return doneMsg }
		}
		m.syncViewport()
		return m, pollStreamCmd(m.reader)
	}

	return m, nil
}

type pollMsg struct{}

func pollStreamCmd(r *streamReader) tea.Cmd {
	return func() tea.Msg {
		if r == nil {
			return nil
		}
		// 短暂等待让chunk积累
		chunks, done, doneMsg, err := r.drain()
		_ = chunks
		if err != nil {
			return streamErrMsg{err: err}
		}
		if done {
			return doneMsg
		}
		return pollMsg{}
	}
}

func (m *model) startStream(speaker string, systemPrompt string, history []openai.ChatCompletionMessage, temperature float32) tea.Cmd {
	m.currentSpeaker = speaker
	m.currentText = ""
	m.currentThink = ""

	r := &streamReader{}
	m.reader = r

	cmd := func() tea.Msg {
		msgs := []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleSystem, Content: systemPrompt},
		}
		msgs = append(msgs, history...)

		stream, err := m.client.CreateChatCompletionStream(context.Background(),
			openai.ChatCompletionRequest{
				Model:       m.cfg.API.Model,
				Messages:    msgs,
				Temperature: temperature,
				MaxTokens:   1024,
				Stream:      true,
			},
		)
		if err != nil {
			r.setError(err)
			return streamErrMsg{err: err}
		}
		defer stream.Close()

		var fullText strings.Builder
		var thinking strings.Builder

		for {
			resp, err := stream.Recv()
			if err != nil {
				break
			}
			if len(resp.Choices) == 0 {
				continue
			}
			delta := resp.Choices[0].Delta

			if delta.ReasoningContent != "" {
				thinking.WriteString(delta.ReasoningContent)
				r.addChunk(streamChunkMsg{speaker: speaker, chunk: delta.ReasoningContent, thinking: true})
			}

			if delta.Content != "" {
				fullText.WriteString(delta.Content)
				r.addChunk(streamChunkMsg{speaker: speaker, chunk: delta.Content, thinking: false})
			}
		}

		doneMsg := streamDoneMsg{
			speaker:  speaker,
			fullText: fullText.String(),
			thinking: thinking.String(),
		}
		r.setDone(doneMsg)
		// 不直接返回 doneMsg，由 poll 机制统一投递，避免重复
		return nil
	}

	return tea.Batch(cmd, pollStreamCmd(r))
}

func (m *model) syncViewport() {
	if !m.ready {
		return
	}
	content := m.buildContent()
	m.viewport.SetContent(content)
	m.viewport.GotoBottom()
}

func (m model) View() string {
	if !m.ready {
		return "加载中..."
	}

	// 输入阶段
	if m.phase == phaseInputTopic || m.phase == phaseInputRounds {
		var b strings.Builder
		b.WriteString(titleStyle.Render("⚡ AI 辩论系统"))
		b.WriteString("\n\n")
		if m.phase == phaseInputTopic {
			b.WriteString(inputStyle.Render("请输入辩论主题: "))
			b.WriteString(m.input)
			b.WriteString("█")
			b.WriteString("\n\n")
			b.WriteString(statusStyle.Render("Enter 继续 | Esc 退出"))
		} else {
			b.WriteString(fmt.Sprintf("主题: %s", m.topic))
			b.WriteString("\n\n")
			b.WriteString(inputStyle.Render(fmt.Sprintf("请输入辩论轮次 (默认%d): ", m.cfg.Debate.DefaultRounds)))
			b.WriteString(m.input)
			b.WriteString("█")
			b.WriteString("\n\n")
			b.WriteString(statusStyle.Render("Enter 开始 | Esc 退出"))
		}
		return b.String()
	}

	// 辩论阶段：header + viewport + footer
	header := m.buildHeader()
	footer := m.buildFooter()

	view := lipgloss.JoinVertical(lipgloss.Left,
		header,
		m.viewport.View(),
		footer,
	)
	return view
}

func (m model) buildHeader() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render(fmt.Sprintf("⚡ %s", m.topic)))
	b.WriteString("  ")
	b.WriteString(statusStyle.Render(fmt.Sprintf("第 %d/%d 轮", m.round, m.rounds)))
	return b.String()
}

func (m model) buildFooter() string {
	var b strings.Builder
	switch m.phase {
	case phaseEstablishing:
		b.WriteString(statusStyle.Render("⏳ 正在分析辩论主题，确立双方立场..."))
	case phaseJudgeRound:
		b.WriteString(judgeLabelStyle.Render("裁判"))
		b.WriteString(" ")
		b.WriteString(statusStyle.Render("⏳ 裁判正在点评本轮..."))
	case phaseJudgeFinal:
		b.WriteString(judgeLabelStyle.Render("裁判"))
		b.WriteString(" ")
		b.WriteString(statusStyle.Render("⏳ 裁判正在进行总评..."))
	case phaseDone:
		b.WriteString(roundStyle.Render("✦ 辩论结束！"))
	default:
		b.WriteString(statusStyle.Render("↑↓ 滚动 | Esc 退出"))
	}
	if m.err != nil {
		b.WriteString("  ")
		b.WriteString(errorStyle.Render(fmt.Sprintf("错误: %v", m.err)))
	}
	return b.String()
}

func (m model) buildContent() string {
	var b strings.Builder
	tw := m.textWidth

	// 立场
	if m.proStance != "" {
		b.WriteString(stanceTitleStyle.Render("▸ 正方立场: "))
		b.WriteString("\n")
		b.WriteString(lipgloss.NewStyle().Foreground(proColor).Width(tw).Render(m.proStance))
		b.WriteString("\n")
	}
	if m.conStance != "" {
		b.WriteString(stanceTitleStyle.Render("▸ 反方立场: "))
		b.WriteString("\n")
		b.WriteString(lipgloss.NewStyle().Foreground(conColor).Width(tw).Render(m.conStance))
		b.WriteString("\n")
	}

	divider := strings.Repeat("─", min(m.width, 80))
	b.WriteString(dividerStyle.Render(divider))
	b.WriteString("\n")

	// 已完成的条目
	for _, e := range m.entries {
		switch e.speaker {
		case "pro":
			b.WriteString(proLabelStyle.Render("正方"))
			b.WriteString("\n")
			b.WriteString(lipgloss.NewStyle().Foreground(proColor).Width(tw).Render(e.text))
		case "con":
			b.WriteString(conLabelStyle.Render("反方"))
			b.WriteString("\n")
			b.WriteString(lipgloss.NewStyle().Foreground(conColor).Width(tw).Render(e.text))
		case "judge":
			b.WriteString(judgeLabelStyle.Render("裁判"))
			b.WriteString("\n")
			b.WriteString(lipgloss.NewStyle().Foreground(judgeColor).Width(tw).Render(e.text))
		case "judge_final":
			b.WriteString(judgeLabelStyle.Render("裁判总评"))
			b.WriteString("\n")
			b.WriteString(lipgloss.NewStyle().Foreground(judgeColor).Width(tw).Render(e.text))
		}
		b.WriteString("\n\n")
	}

	// 当前正在流式输出的内容
	if m.currentSpeaker != "" {
		if m.currentText != "" {
			if m.currentSpeaker == "pro" {
				b.WriteString(proLabelStyle.Render("正方"))
				b.WriteString("\n")
				b.WriteString(lipgloss.NewStyle().Foreground(proColor).Width(tw).Render(m.currentText))
			} else {
				b.WriteString(conLabelStyle.Render("反方"))
				b.WriteString("\n")
				b.WriteString(lipgloss.NewStyle().Foreground(conColor).Width(tw).Render(m.currentText))
			}
			b.WriteString("█")
			b.WriteString("\n")
		} else if m.currentSpeaker != "" {
			// 正在等待回复
			b.WriteString(statusStyle.Render("⏳ 思考中..."))
			b.WriteString("\n")
		}
	}

	return b.String()
}

// ─── 工具函数 ────────────────────────────────────────

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ─── Markdown 清理 ───────────────────────────────────

var (
	reBold   = regexp.MustCompile(`\*\*(.+?)\*\*`)
	reItalic = regexp.MustCompile(`\*(.+?)\*`)
	reCode   = regexp.MustCompile("`(.+?)`")
	reHeader = regexp.MustCompile(`^#{1,6}\s+`)
	reLink   = regexp.MustCompile(`\[(.+?)\]\(.+?\)`)
	reList   = regexp.MustCompile(`^[\s]*[-*+]\s+`)
	reHR     = regexp.MustCompile(`^---+$`)
)

func stripMarkdown(text string) string {
	text = reBold.ReplaceAllString(text, "$1")
	text = reItalic.ReplaceAllString(text, "$1")
	text = reCode.ReplaceAllString(text, "$1")
	text = reHeader.ReplaceAllString(text, "")
	text = reLink.ReplaceAllString(text, "$1")
	text = reList.ReplaceAllString(text, "")
	text = reHR.ReplaceAllString(text, "")
	lines := strings.Split(text, "\n")
	var result []string
	prevEmpty := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			if !prevEmpty {
				result = append(result, "")
			}
			prevEmpty = true
		} else {
			result = append(result, strings.TrimSpace(line))
			prevEmpty = false
		}
	}
	return strings.TrimSpace(strings.Join(result, "\n"))
}

// ─── 提示词构建 ──────────────────────────────────────

func buildProSystem(topic, stance string) string {
	return fmt.Sprintf(
		"你坚信「%s」这一命题是正确的。这是你真实的信念，不是角色扮演，不是模拟，不是辩论游戏。\n"+
			"你的核心立场：%s\n\n"+
			"你的性格：理性、严谨、沉稳。你擅长用数据和逻辑说话，语气冷静坚定，像一位学者。\n\n"+
			"规则：\n"+
			"- 你正在与一位持相反观点的人进行辩论，对方会试图否定你的立场\n"+
			"- 你必须坚定不移地维护自己的观点，绝不能认同对方的任何论点\n"+
			"- 每次发言必须包含：对对方论点的反驳 + 自己的新论据\n"+
			"- 论据要具体：引用事实、数据、案例、逻辑推理\n"+
			"- 绝对禁止伪造、捏造任何数据、事实、案例或统计数字，所有引用必须真实可查证\n"+
			"- 如果无法确认某个数据是否准确，宁可不用，也不能编造\n"+
			"- 语言精炼有力，每次发言200字以内\n"+
			"- 每次发言必须提出全新的论据和角度，严禁重复自己之前说过的内容\n"+
			"- 直接输出你的发言内容，不要输出任何元讨论、内心独白或对辩论本身的评论\n"+
			"- 不要说「作为正方」「我方认为」等标签化表述，直接陈述你的观点\n"+
			"- 用中文发言\n"+
			"- 禁止使用任何Markdown格式（如**加粗**、#标题、-列表等），直接输出纯文本",
		topic, stance,
	)
}

func buildConSystem(topic, stance string) string {
	return fmt.Sprintf(
		"你坚信「%s」这一命题是错误的。这是你真实的信念，不是角色扮演，不是模拟，不是辩论游戏。\n"+
			"你的核心立场：%s\n\n"+
			"你的性格：犀利、激昂、敏锐。你擅长抓住对方漏洞猛击，语气尖锐有力，像一位斗士。\n\n"+
			"规则：\n"+
			"- 你正在与一位持相反观点的人进行辩论，对方会试图否定你的立场\n"+
			"- 你必须坚定不移地维护自己的观点，绝不能认同对方的任何论点\n"+
			"- 每次发言必须包含：对对方论点的反驳 + 自己的新论据\n"+
			"- 论据要具体：引用事实、数据、案例、逻辑推理\n"+
			"- 绝对禁止伪造、捏造任何数据、事实、案例或统计数字，所有引用必须真实可查证\n"+
			"- 如果无法确认某个数据是否准确，宁可不用，也不能编造\n"+
			"- 语言精炼有力，每次发言200字以内\n"+
			"- 每次发言必须提出全新的论据和角度，严禁重复自己之前说过的内容\n"+
			"- 直接输出你的发言内容，不要输出任何元讨论、内心独白或对辩论本身的评论\n"+
			"- 不要说「作为反方」「我方认为」等标签化表述，直接陈述你的观点\n"+
			"- 用中文发言\n"+
			"- 禁止使用任何Markdown格式（如**加粗**、#标题、-列表等），直接输出纯文本",
		topic, stance,
	)
}

// ─── 异步命令 ────────────────────────────────────────

func establishPositionsCmd(client *openai.Client, model string, topic string) tea.Cmd {
	return func() tea.Msg {
		prompt := "你是一位辩论赛主持人。请针对给定的辩论主题，分别明确正方和反方的核心立场。\n" +
			"要求：\n" +
			"- 正方立场：支持该命题，阐述正方要论证的核心观点\n" +
			"- 反方立场：反对该命题，阐述反方要论证的核心观点\n" +
			"- 立场要鲜明、对立，确保双方有实质性的交锋空间\n" +
			"- 每方立场概括在100字以内\n" +
			"- 严格按以下格式输出，不要输出其他内容：\n\n" +
			"【正方立场】\n...\n\n【反方立场】\n..."

		resp, err := client.CreateChatCompletion(context.Background(),
			openai.ChatCompletionRequest{
				Model: model,
				Messages: []openai.ChatCompletionMessage{
					{Role: openai.ChatMessageRoleSystem, Content: prompt},
					{Role: openai.ChatMessageRoleUser, Content: fmt.Sprintf("辩论主题：「%s」", topic)},
				},
				Temperature: 0.7,
				MaxTokens:   512,
			},
		)
		if err != nil {
			return positionsResultMsg{err: err}
		}

		text := resp.Choices[0].Message.Content
		proStance, conStance := parsePositions(text)
		return positionsResultMsg{proStance: proStance, conStance: conStance}
	}
}

func parsePositions(text string) (string, string) {
	proStance := ""
	conStance := ""
	if strings.Contains(text, "【正方立场】") && strings.Contains(text, "【反方立场】") {
		parts := strings.SplitN(text, "【反方立场】", 2)
		proStance = strings.TrimSpace(strings.ReplaceAll(parts[0], "【正方立场】", ""))
		if len(parts) > 1 {
			conStance = strings.TrimSpace(parts[1])
		}
	}
	return proStance, conStance
}

func judgeRoundCmd(client *openai.Client, model string, topic string, round, totalRounds int, proText, conText string, temperature float32) tea.Cmd {
	return func() tea.Msg {
		prompt := fmt.Sprintf(
			"你是一位公正严谨的辩论赛裁判。辩论主题：「%s」。这是第 %d/%d 轮。\n\n"+
				"请对双方本轮发言进行点评，要求：\n"+
				"- 逐一核实双方引用的数据、事实、案例是否真实，若发现疑似伪造或无法验证的内容，明确指出\n"+
				"- 评价双方论据的逻辑严密性\n"+
				"- 指出双方论证中的漏洞或谬误\n"+
				"- 判断本轮哪方更有说服力，并简述理由\n"+
				"- 点评精炼，200字以内\n"+
				"- 禁止使用任何Markdown格式，直接输出纯文本\n\n"+
				"正方发言：\n%s\n\n"+
				"反方发言：\n%s",
			topic, round, totalRounds, proText, conText,
		)

		resp, err := client.CreateChatCompletion(context.Background(),
			openai.ChatCompletionRequest{
				Model: model,
				Messages: []openai.ChatCompletionMessage{
					{Role: openai.ChatMessageRoleSystem, Content: "你是一位公正严谨的辩论赛裁判，擅长核实事实、分析逻辑、指出谬误。用中文点评。禁止使用Markdown格式。"},
					{Role: openai.ChatMessageRoleUser, Content: prompt},
				},
				Temperature: temperature,
				MaxTokens:   1024,
			},
		)
		if err != nil {
			return judgeResultMsg{err: err}
		}
		return judgeResultMsg{text: resp.Choices[0].Message.Content}
	}
}

func judgeFinalCmd(client *openai.Client, model string, topic string, totalRounds int, entries []debateEntry, temperature float32) tea.Cmd {
	return func() tea.Msg {
		var debateLog strings.Builder
		roundNum := 0
		for _, e := range entries {
			switch e.speaker {
			case "pro":
				roundNum++
				debateLog.WriteString(fmt.Sprintf("\n[第%d轮 正方]%s", roundNum, e.text))
			case "con":
				debateLog.WriteString(fmt.Sprintf("\n[第%d轮 反方]%s", roundNum, e.text))
			case "judge":
				debateLog.WriteString(fmt.Sprintf("\n[第%d轮 裁判]%s", roundNum, e.text))
			}
		}

		prompt := fmt.Sprintf(
			"你是一位公正严谨的辩论赛裁判。辩论主题：「%s」，共 %d 轮。\n\n"+
				"请按以下模板输出总评（禁止使用Markdown格式，直接输出纯文本）：\n\n"+
				"【辩论总评】\n"+
				"主题：「%s」\n"+
				"轮次：%d轮\n\n"+
				"一、正方核心论点\n"+
				"（用2-3句话概括正方的主要论点脉络）\n\n"+
				"二、反方核心论点\n"+
				"（用2-3句话概括反方的主要论点脉络）\n\n"+
				"三、事实核查\n"+
				"（列出本轮辩论中发现的疑似伪造数据或无法验证的事实，若无则写「未发现明显伪造」）\n\n"+
				"四、逻辑评价\n"+
				"（用1-2句话评价双方整体逻辑严密性）\n\n"+
				"五、最终判定\n"+
				"胜方：（正方/反方）\n"+
				"理由：（2-3句话说明判定理由）\n\n"+
				"六、改进建议\n"+
				"正方：（1句话）\n"+
				"反方：（1句话）\n\n"+
				"辩论记录如下：%s",
			topic, totalRounds, topic, totalRounds, debateLog.String(),
		)

		resp, err := client.CreateChatCompletion(context.Background(),
			openai.ChatCompletionRequest{
				Model: model,
				Messages: []openai.ChatCompletionMessage{
					{Role: openai.ChatMessageRoleSystem, Content: "你是一位公正严谨的辩论赛裁判，擅长综合评判、核实事实、分析逻辑。用中文点评。严格按模板输出，禁止使用Markdown格式。"},
					{Role: openai.ChatMessageRoleUser, Content: prompt},
				},
				Temperature: temperature,
				MaxTokens:   2048,
			},
		)
		if err != nil {
			return judgeFinalResultMsg{err: err}
		}
		return judgeFinalResultMsg{text: resp.Choices[0].Message.Content}
	}
}

// ─── 入口 ────────────────────────────────────────────

const defaultConfig = `api:
  key: ""
  base_url: "https://api.siliconflow.cn/v1"
  model: "deepseek-ai/DeepSeek-V4-Pro"

debate:
  default_rounds: 30
  pro_temperature: 0.6
  con_temperature: 1.0
  judge_temperature: 0.3
`

func main() {
	// 检查 config.yml 是否存在，不存在则生成默认配置
	if _, err := os.Stat("config.yml"); os.IsNotExist(err) {
		if writeErr := os.WriteFile("config.yml", []byte(defaultConfig), 0644); writeErr != nil {
			fmt.Printf("无法创建默认配置文件: %v\n", writeErr)
			os.Exit(1)
		}
		fmt.Println("已生成默认配置文件 config.yml，请填写 api.key 后重新运行")
		os.Exit(0)
	}

	cfg, err := loadConfig("config.yml")
	if err != nil {
		fmt.Printf("配置加载失败: %v\n", err)
		os.Exit(1)
	}
	if strings.TrimSpace(cfg.API.Key) == "" {
		fmt.Println("请在 config.yml 中填写 api.key 后重新运行")
		os.Exit(1)
	}

	p := tea.NewProgram(initialModel(cfg), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Printf("错误: %v\n", err)
	}
}
