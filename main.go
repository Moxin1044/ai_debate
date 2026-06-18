package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/jung-kurt/gofpdf"
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

	modeStyle = lipgloss.NewStyle().
			Foreground(accentColor).
			Bold(true)

	modeDescStyle = lipgloss.NewStyle().
			Foreground(dimColor)

	exportStyle = lipgloss.NewStyle().
			Foreground(accentColor)
)

// ─── 阶段与消息类型 ─────────────────────────────────

type phase int

const (
	phaseInputTopic phase = iota
	phaseSelectMode
	phaseInputRounds
	phaseInputStance
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
	stepType stepType
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
	stepType stepType
	label    string
	text     string
	thinking string
}

// ─── 辩论模式定义 ────────────────────────────────────

type stepType int

const (
	stepOpening        stepType = iota // 开篇立论
	stepConstructive                   // 立论/反驳
	stepCrossExamination               // 交叉质询
	stepRebuttal                       // 反驳
	stepClosing                        // 结辩
)

func (s stepType) String() string {
	switch s {
	case stepOpening:
		return "开篇立论"
	case stepConstructive:
		return "立论"
	case stepCrossExamination:
		return "交叉质询"
	case stepRebuttal:
		return "反驳"
	case stepClosing:
		return "结辩"
	default:
		return "发言"
	}
}

type modeStep struct {
	typ     stepType
	speaker string // "pro" or "con"
	label   string // display label
}

type debateMode struct {
	id     string
	name   string
	desc   string
	fixedRounds bool // true = 固定轮数，不询问用户
	rounds int      // 默认/固定轮数（主辩论环节）
	steps []modeStep // 辩论流程的步骤序列
}

var debateModes = []debateMode{
	{
		id:     "free",
		name:   "自由辩论",
		desc:   "双方自由交替发言，每轮可提出全新论点，灵活多变",
		fixedRounds: false,
		rounds: 30,
		// steps 动态生成：pro/con 交替 * rounds
	},
	{
		id:     "policy",
		name:   "政策辩论",
		desc:   "正方提出政策方案 → 反方质询 → 反方提出替代方案 → 正方质询 → 双方反驳 → 结辩",
		fixedRounds: true,
		rounds: 3, // 反驳轮次
		steps: []modeStep{
			{stepOpening, "pro", "正方政策方案"},
			{stepCrossExamination, "con", "反方质询正方"},
			{stepConstructive, "con", "反方替代方案"},
			{stepCrossExamination, "pro", "正方质询反方"},
			// 后面动态插入 rebuttal rounds
		},
	},
	{
		id:     "oxford",
		name:   "牛津式辩论",
		desc:   "开篇立论 → 交叉质询 → 结辩陈词，经典英式辩论赛制",
		fixedRounds: true,
		rounds: 3, // 交叉质询轮次
		steps: []modeStep{
			{stepOpening, "pro", "正方开篇立论"},
			{stepOpening, "con", "反方开篇立论"},
			// 后面动态插入 cross-examination rounds
		},
	},
	{
		id:     "lincoln-douglas",
		name:   "林肯-道格拉斯式",
		desc:   "正方立论 → 反方质询 → 反方立论 → 正方质询 → 反驳 → 结辩，美式一对一制",
		fixedRounds: true,
		rounds: 2, // 额外反驳轮次
		steps: []modeStep{
			{stepConstructive, "pro", "正方立论"},
			{stepCrossExamination, "con", "反方质询正方"},
			{stepConstructive, "con", "反方立论"},
			{stepCrossExamination, "pro", "正方质询反方"},
			{stepRebuttal, "pro", "正方反驳"},
			{stepRebuttal, "con", "反方反驳"},
			// 后面动态插入额外 rebuttal rounds
		},
	},
}

// 为自由辩论模式生成步骤
func buildFreeSteps(rounds int) []modeStep {
	steps := make([]modeStep, 0, rounds*2)
	for i := 1; i <= rounds; i++ {
		steps = append(steps, modeStep{stepConstructive, "pro", fmt.Sprintf("第%d轮 正方", i)})
		steps = append(steps, modeStep{stepConstructive, "con", fmt.Sprintf("第%d轮 反方", i)})
	}
	return steps
}

// 为固定模式补充动态步骤（质询/反驳轮次 + 结辩）
func buildFixedModeSteps(mode debateMode, rounds int) []modeStep {
	steps := mode.steps
	switch mode.id {
	case "policy":
		// 插入 N 轮反驳
		for i := 1; i <= rounds; i++ {
			steps = append(steps, modeStep{stepRebuttal, "pro", fmt.Sprintf("反驳第%d轮 正方", i)})
			steps = append(steps, modeStep{stepRebuttal, "con", fmt.Sprintf("反驳第%d轮 反方", i)})
		}
		// 结辩
		steps = append(steps, modeStep{stepClosing, "con", "反方结辩"})
		steps = append(steps, modeStep{stepClosing, "pro", "正方结辩"})
	case "oxford":
		// 插入 N 轮交叉质询
		for i := 1; i <= rounds; i++ {
			steps = append(steps, modeStep{stepCrossExamination, "pro", fmt.Sprintf("第%d轮 正方质询", i)})
			steps = append(steps, modeStep{stepCrossExamination, "con", fmt.Sprintf("第%d轮 反方质询", i)})
		}
		// 结辩（牛津式：反方先结辩，正方后结辩）
		steps = append(steps, modeStep{stepClosing, "con", "反方结辩"})
		steps = append(steps, modeStep{stepClosing, "pro", "正方结辩"})
	case "lincoln-douglas":
		// 插入 N 轮额外反驳
		for i := 1; i <= rounds; i++ {
			steps = append(steps, modeStep{stepRebuttal, "pro", fmt.Sprintf("追加反驳第%d轮 正方", i)})
			steps = append(steps, modeStep{stepRebuttal, "con", fmt.Sprintf("追加反驳第%d轮 反方", i)})
		}
		// 结辩（LD式：反方先结辩，正方后结辩）
		steps = append(steps, modeStep{stepClosing, "con", "反方结辩"})
		steps = append(steps, modeStep{stepClosing, "pro", "正方结辩"})
	}
	return steps
}

// ─── 流式读取器 ──────────────────────────────────────

type streamReader struct {
	mu      sync.Mutex
	chunks  []streamChunkMsg
	done    bool
	doneMsg streamDoneMsg
	err     error
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
	phase phase
	topic string

	modeIndex   int  // 选中的模式索引
	currentMode debateMode
	modeSteps   []modeStep
	modeStepN   int // 当前步骤在 modeSteps 中的索引

	rounds int
	round  int

	proStance string
	conStance string

	// 自定义立场
	customProStance string
	customConStance string

	input string

	entries        []debateEntry
	currentSpeaker string
	currentText    string
	currentThink   string

	currentStepType stepType

	proTextLatest string
	conTextLatest string

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

	reader    *streamReader
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
		return m.handleKeyMsg(msg)

	case positionsResultMsg:
		if msg.err != nil {
			m.err = msg.err
			m.phase = phaseInputTopic
			return m, nil
		}
		m.proStance = msg.proStance
		m.conStance = msg.conStance
		return m.startDebate()

	case streamDoneMsg:
		fullText := stripMarkdown(msg.fullText)
		thinking := stripMarkdown(msg.thinking)
		m.entries = append(m.entries, debateEntry{
			speaker:  msg.speaker,
			stepType: msg.stepType,
			label:    m.currentStepLabel(),
			text:     fullText,
			thinking: thinking,
		})
		m.currentText = ""
		m.currentThink = ""
		m.currentSpeaker = ""
		m.reader = nil

		// 根据发言者记录最新内容
		if msg.speaker == "pro" {
			m.proTextLatest = msg.fullText
		} else {
			m.conTextLatest = msg.fullText
		}

		// 更新历史
		return m.advanceDebate(msg)

	case judgeResultMsg:
		if msg.err != nil {
			m.err = msg.err
		} else {
			m.entries = append(m.entries, debateEntry{
				speaker:  "judge",
				stepType: stepConstructive,
				label:    fmt.Sprintf("第%d轮裁判点评", m.round),
				text:     stripMarkdown(msg.text),
				thinking: stripMarkdown(msg.thinking),
			})
		}
		return m.afterJudgeRound()

	case judgeFinalResultMsg:
		if msg.err != nil {
			m.err = msg.err
		} else {
			m.entries = append(m.entries, debateEntry{
				speaker:  "judge_final",
				stepType: stepConstructive,
				label:    "裁判总评",
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

func (m *model) handleKeyMsg(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// 输入阶段
	if m.phase == phaseInputTopic || m.phase == phaseInputRounds || m.phase == phaseInputStance {
		switch msg.Type {
		case tea.KeyCtrlC:
			return m, tea.Quit
		case tea.KeyEnter:
			return m.handleEnter()
		case tea.KeyBackspace:
			if len(m.input) > 0 {
				m.input = m.input[:len(m.input)-1]
			}
			return m, nil
		default:
			m.input += msg.String()
			return m, nil
		}
	}

	// 模式选择阶段
	if m.phase == phaseSelectMode {
		switch msg.Type {
		case tea.KeyCtrlC:
			return m, tea.Quit
		case tea.KeyUp:
			if m.modeIndex > 0 {
				m.modeIndex--
			}
			return m, nil
		case tea.KeyDown:
			if m.modeIndex < len(debateModes)-1 {
				m.modeIndex++
			}
			return m, nil
		case tea.KeyEnter:
			mode := debateModes[m.modeIndex]
			m.currentMode = mode
			m.phase = phaseInputRounds
			m.input = ""
			return m, nil
		}
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
}

func (m *model) handleEnter() (tea.Model, tea.Cmd) {
	switch m.phase {
	case phaseInputTopic:
		m.topic = strings.TrimSpace(m.input)
		if m.topic == "" {
			return m, nil
		}
		m.input = ""
		m.phase = phaseSelectMode
		return m, nil

	case phaseInputRounds:
		input := strings.TrimSpace(m.input)
		if input == "" {
			if m.currentMode.fixedRounds && m.currentMode.rounds > 0 {
				m.rounds = m.currentMode.rounds
			} else {
				m.rounds = m.cfg.Debate.DefaultRounds
			}
		} else {
			n, err := strconv.Atoi(input)
			if err != nil || n <= 0 {
				m.err = fmt.Errorf("请输入有效的正整数")
				return m, nil
			}
			m.rounds = n
		}
		m.input = ""
		m.phase = phaseInputStance
		return m, nil

	case phaseInputStance:
		stanceInput := strings.TrimSpace(m.input)
		if stanceInput != "" {
			// 解析用户输入的正反方立场，格式：正方:xxx;反方:xxx
			parseStance(stanceInput, &m.customProStance, &m.customConStance)
		}
		m.input = ""

		if m.customProStance != "" && m.customConStance != "" {
			m.proStance = m.customProStance
			m.conStance = m.customConStance
			return m.startDebate()
		}

		// 没有自定义立场，让 AI 确立
		m.phase = phaseEstablishing
		return m, establishPositionsCmd(m.client, m.cfg.API.Model, m.topic)
	}

	return m, nil
}

func (m *model) startDebate() (tea.Model, tea.Cmd) {
	// 构建步骤
	switch m.currentMode.id {
	case "free":
		m.modeSteps = buildFreeSteps(m.rounds)
	default:
		m.modeSteps = buildFixedModeSteps(m.currentMode, m.rounds)
	}

	m.proSystem = buildProSystem(m.topic, m.proStance)
	m.conSystem = buildConSystem(m.topic, m.conStance)
	m.phase = phaseDebating
	m.modeStepN = 0
	m.round = 1
	m.proHistory = nil
	m.conHistory = nil

	firstStep := m.modeSteps[0]
	return m, m.startFirstStep(firstStep)
}

func (m *model) startFirstStep(s modeStep) tea.Cmd {
	var sysPrompt string
	var history []openai.ChatCompletionMessage
	var temperature float32

	switch s.typ {
	case stepOpening:
		sysPrompt = buildOpeningSystem(s.speaker, m.topic, m.proStance)
		history = []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleUser, Content: fmt.Sprintf("请围绕「%s」进行开篇立论。", m.topic)},
		}
		temperature = m.cfg.Debate.ProTemperature
	default:
		sysPrompt = m.proSystem
		history = []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleUser, Content: fmt.Sprintf("请阐述你对「%s」的观点。", m.topic)},
		}
		temperature = m.cfg.Debate.ProTemperature
	}
	return m.startStream(s.speaker, sysPrompt, history, temperature, s.typ)
}

func (m *model) advanceDebate(msg streamDoneMsg) (tea.Model, tea.Cmd) {
	// 记录发言历史
	speaker := msg.speaker
	otherHistory := &m.conHistory
	selfHistory := &m.proHistory
	if speaker == "con" {
		otherHistory = &m.proHistory
		selfHistory = &m.conHistory
	}

	*selfHistory = append(*selfHistory, openai.ChatCompletionMessage{
		Role: openai.ChatMessageRoleAssistant, Content: msg.fullText,
	})

	*otherHistory = append(*otherHistory, openai.ChatCompletionMessage{
		Role: openai.ChatMessageRoleUser,
		Content: fmt.Sprintf("对方发言：%s\n\n请回应对方。", msg.fullText),
	})

	// 前进到下一步
	m.modeStepN++

	if m.modeStepN >= len(m.modeSteps) {
		// 所有步骤完成 → 每轮裁判 + 最终总评
		m.phase = phaseJudgeRound
		m.syncViewport()
		return m, judgeRoundCmd(m.client, m.cfg.API.Model, m.topic, m.round, m.rounds,
			m.proTextLatest, m.conTextLatest, m.cfg.Debate.JudgeTemperature)
	}

	nextStep := m.modeSteps[m.modeStepN]

	// 计算"第几轮"（仅在自由辩论模式下有意义）
	if m.currentMode.id == "free" && nextStep.speaker == "pro" {
		m.round++
	}

	// 对交叉质询，给对方特殊的 prompt
	var history []openai.ChatCompletionMessage
	var sysPrompt string
	var temperature float32

	// actualSpeaker：实际发言的 AI；labelSpeaker：步骤标签指示的发言方
	actualSpeaker := nextStep.speaker

	if nextStep.typ == stepCrossExamination {
		// 交叉质询：labelSpeaker 是质询方，但 AI call 返回的是被质询方的回答
		// 例如 "正方质询反方"：step.speaker="pro"（标签），但回答的是反方
		if nextStep.speaker == "pro" {
			actualSpeaker = "con"
			sysPrompt = buildCrossExamResponderSystem("pro", m.topic, m.conStance, m.proStance)
			history = m.conHistory
			temperature = m.cfg.Debate.ConTemperature
		} else {
			actualSpeaker = "pro"
			sysPrompt = buildCrossExamResponderSystem("con", m.topic, m.proStance, m.conStance)
			history = m.proHistory
			temperature = m.cfg.Debate.ProTemperature
		}
	} else {
		if nextStep.speaker == "pro" {
			sysPrompt = m.proSystem
			history = m.proHistory
			temperature = m.cfg.Debate.ProTemperature
		} else {
			sysPrompt = m.conSystem
			history = m.conHistory
			temperature = m.cfg.Debate.ConTemperature
		}
	}

	m.syncViewport()
	return m, m.startStream(actualSpeaker, sysPrompt, history, temperature, nextStep.typ)
}

func (m *model) afterJudgeRound() (tea.Model, tea.Cmd) {
	// 在自由辩论模式下，按轮次检查
	if m.currentMode.id == "free" && m.round < m.rounds {
		// 不推进 round，因为每轮有两步
		// 实际上在 advanceDebate 中已推进了
		m.round++ // 弥补
	}

	// 检查是否还有更多步骤
	if m.modeStepN >= len(m.modeSteps) {
		m.phase = phaseJudgeFinal
		m.syncViewport()
		return m, judgeFinalCmd(m.client, m.cfg.API.Model, m.topic, m.rounds, m.entries, m.cfg.Debate.JudgeTemperature)
	}

	m.phase = phaseDebating
	m.syncViewport()
	return m, nil
}

func (m model) currentStepLabel() string {
	if m.modeStepN < len(m.modeSteps) {
		return m.modeSteps[m.modeStepN].label
	}
	return ""
}

// ─── 视图 ────────────────────────────────────────────

func (m model) View() string {
	if !m.ready {
		return "加载中..."
	}

	// 输入阶段
	if m.phase == phaseInputTopic || m.phase == phaseInputRounds || m.phase == phaseInputStance {
		return m.renderInputView()
	}

	// 模式选择阶段
	if m.phase == phaseSelectMode {
		return m.renderModeSelectView()
	}

	// 辩论阶段
	header := m.buildHeader()
	footer := m.buildFooter()

	view := lipgloss.JoinVertical(lipgloss.Left,
		header,
		m.viewport.View(),
		footer,
	)
	return view
}

func (m model) renderInputView() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("⚡ AI 辩论系统"))
	b.WriteString("\n\n")

	switch m.phase {
	case phaseInputTopic:
		b.WriteString(inputStyle.Render("请输入辩论主题: "))
		b.WriteString(m.input)
		b.WriteString("█")
		b.WriteString("\n\n")
		b.WriteString(statusStyle.Render("Enter 继续 | Esc 退出"))

	case phaseInputRounds:
		b.WriteString(fmt.Sprintf("主题: %s", m.topic))
		b.WriteString("\n")
		b.WriteString(fmt.Sprintf("模式: %s", m.currentMode.name))
		b.WriteString("\n\n")
		defaultRounds := m.cfg.Debate.DefaultRounds
		if m.currentMode.fixedRounds && m.currentMode.rounds > 0 {
			defaultRounds = m.currentMode.rounds
		}
		b.WriteString(inputStyle.Render(fmt.Sprintf("请输入辩论轮次 (默认%d): ", defaultRounds)))
		b.WriteString(m.input)
		b.WriteString("█")
		b.WriteString("\n\n")
		b.WriteString(statusStyle.Render("Enter 开始 | Esc 退出"))

	case phaseInputStance:
		b.WriteString(fmt.Sprintf("主题: %s", m.topic))
		b.WriteString("\n")
		b.WriteString(fmt.Sprintf("模式: %s", m.currentMode.name))
		b.WriteString("\n\n")
		b.WriteString(statusStyle.Render("可选：输入自定义正反方立场，格式: "))
		b.WriteString(accentColorStr("正方:xxx;反方:xxx"))
		b.WriteString("\n")
		b.WriteString(statusStyle.Render("留空则自动生成立场"))
		b.WriteString("\n\n")
		b.WriteString(inputStyle.Render("立场: "))
		b.WriteString(m.input)
		b.WriteString("█")
		b.WriteString("\n\n")
		b.WriteString(statusStyle.Render("Enter 确认 | Esc 退出"))
	}

	if m.err != nil {
		b.WriteString("\n")
		b.WriteString(errorStyle.Render(fmt.Sprintf("错误: %v", m.err)))
	}
	return b.String()
}

func (m model) renderModeSelectView() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("⚡ AI 辩论系统"))
	b.WriteString("\n\n")
	b.WriteString(fmt.Sprintf("主题: %s", m.topic))
	b.WriteString("\n\n")
	b.WriteString(inputStyle.Render("请选择辩论模式 (↑↓选择 Enter确认):"))
	b.WriteString("\n\n")

	for i, mode := range debateModes {
		prefix := "  "
		if i == m.modeIndex {
			prefix = accentedStr("▶ ")
		}
		b.WriteString(fmt.Sprintf("%s%s", prefix, modeStyle.Render(mode.name)))
		b.WriteString("\n")
		b.WriteString(fmt.Sprintf("%s  %s", prefix, modeDescStyle.Render(mode.desc)))
		b.WriteString("\n\n")
	}

	return b.String()
}

func (m model) buildHeader() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render(fmt.Sprintf("⚡ %s", m.topic)))
	b.WriteString("  ")
	b.WriteString(modeStyle.Render(fmt.Sprintf("[%s]", m.currentMode.name)))
	b.WriteString("  ")
	if m.currentMode.id == "free" {
		b.WriteString(statusStyle.Render(fmt.Sprintf("第 %d/%d 轮", m.round, m.rounds)))
	} else {
		step := m.modeStepN + 1
		total := len(m.modeSteps)
		b.WriteString(statusStyle.Render(fmt.Sprintf("步骤 %d/%d", step, total)))
	}
	return b.String()
}

func (m model) buildFooter() string {
	var b strings.Builder
	switch m.phase {
	case phaseEstablishing:
		b.WriteString(statusStyle.Render("⏳ 正在分析辩论主题，确立双方立场..."))
	case phaseDebating:
		if m.modeStepN < len(m.modeSteps) {
			b.WriteString(statusStyle.Render(fmt.Sprintf("⏳ %s进行中...", m.modeSteps[m.modeStepN].label)))
		}
	case phaseJudgeRound:
		b.WriteString(judgeLabelStyle.Render("裁判"))
		b.WriteString(" ")
		b.WriteString(statusStyle.Render("⏳ 裁判正在点评..."))
	case phaseJudgeFinal:
		b.WriteString(judgeLabelStyle.Render("裁判"))
		b.WriteString(" ")
		b.WriteString(statusStyle.Render("⏳ 裁判正在进行总评..."))
	case phaseDone:
		b.WriteString(roundStyle.Render("✦ 辩论结束！"))
		b.WriteString("  ")
		b.WriteString(exportStyle.Render("按 E 导出报告"))
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

	// 已完成条目
	for _, e := range m.entries {
		switch e.speaker {
		case "pro":
			if e.label != "" {
				b.WriteString(roundStyle.Render(fmt.Sprintf("▸ %s", e.label)))
				b.WriteString("\n")
			}
			b.WriteString(proLabelStyle.Render("正方"))
			b.WriteString("\n")
			b.WriteString(lipgloss.NewStyle().Foreground(proColor).Width(tw).Render(e.text))
		case "con":
			if e.label != "" {
				b.WriteString(roundStyle.Render(fmt.Sprintf("▸ %s", e.label)))
				b.WriteString("\n")
			}
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

	// 当前流式输出
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
		} else {
			b.WriteString(statusStyle.Render("⏳ 思考中..."))
			b.WriteString("\n")
		}
	}

	return b.String()
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
			"- 每次发言必须包含：对对方论点的回应 + 自己的论据\n"+
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
			"- 每次发言必须包含：对对方论点的回应 + 自己的论据\n"+
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

func buildOpeningSystem(speaker, topic, stance string) string {
	role := "正方"
	correct := "正确"
	if speaker == "con" {
		role = "反方"
		correct = "错误"
	}
	return fmt.Sprintf(
		"你坚信「%s」这一命题是%s的。这是你的开篇立论，你需要全面阐述%s的核心立场和主要论据。\n"+
			"你的核心立场：%s\n\n"+
			"规则：\n"+
			"- 这是你第一次发言，对方还没有发言，因此不需要回应对手\n"+
			"- 全面系统地阐述你的核心理念和主要论据\n"+
			"- 论据真实可查，禁止伪造数据\n"+
			"- 语言精炼，300字以内\n"+
			"- 直接用中文发言，禁止Markdown格式",
		topic, correct, role, stance,
	)
}

func buildCrossExamResponderSystem(questioner, topic, answererStance, questionerStance string) string {
	return fmt.Sprintf(
		"你正在参与关于「%s」的辩论，当前是交叉质询环节，对方正在向你提问。\n"+
			"你的立场：%s\n"+
			"对方立场：%s\n\n"+
			"规则：\n"+
			"- 你需要坚定地捍卫自己的立场，回应对方的质询\n"+
			"- 对对方可能提出的质疑给出有力的答复\n"+
			"- 指出对方立场中的问题\n"+
			"- 150字以内\n"+
			"- 直接用中文发言，禁止Markdown格式",
		topic, answererStance, questionerStance,
	)
}

func buildClosingSystem(speaker, topic, stance string) string {
	role := "正方"
	if speaker == "con" {
		role = "反方"
	}
	return fmt.Sprintf(
		"你坚信「%s」这一命题的%s方立场。这是你的结辩陈词，辩论即将结束。\n"+
			"你的核心立场：%s\n\n"+
			"规则：\n"+
			"- 总结你在整场辩论中的最有力论据\n"+
			"- 指出对方论点的根本性缺陷\n"+
			"- 用有力的总结打动听众，坚定你%s的立场\n"+
			"- 语言精炼有力，200字以内\n"+
			"- 不要说「作为某方」「综上所述」等标签，直接有力陈述\n"+
			"- 用中文，禁止Markdown格式",
		topic, role, stance, role,
	)
}

// ─── 流式调用 ────────────────────────────────────────

func (m *model) startStream(speaker string, systemPrompt string, history []openai.ChatCompletionMessage, temperature float32, st stepType) tea.Cmd {
	m.currentSpeaker = speaker
	m.currentText = ""
	m.currentThink = ""
	m.currentStepType = st

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
			stepType: st,
			fullText: fullText.String(),
			thinking: thinking.String(),
		}
		r.setDone(doneMsg)
		return nil
	}

	return tea.Batch(cmd, pollStreamCmd(r))
}

type pollMsg struct{}

func pollStreamCmd(r *streamReader) tea.Cmd {
	return func() tea.Msg {
		if r == nil {
			return nil
		}
		_, done, doneMsg, err := r.drain()
		if err != nil {
			return streamErrMsg{err: err}
		}
		if done {
			return doneMsg
		}
		return pollMsg{}
	}
}

func (m *model) syncViewport() {
	if !m.ready {
		return
	}
	content := m.buildContent()
	m.viewport.SetContent(content)
	m.viewport.GotoBottom()
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

func parseStance(input string, pro, con *string) {
	input = strings.TrimSpace(input)
	// 尝试解析 正方:xxx;反方:xxx 格式
	re := regexp.MustCompile(`正方[：:]\s*(.+?)\s*[；;]\s*反方[：:]\s*(.+)`)
	m := re.FindStringSubmatch(input)
	if len(m) == 3 {
		*pro = strings.TrimSpace(m[1])
		*con = strings.TrimSpace(m[2])
	}
}

func judgeRoundCmd(client *openai.Client, model string, topic string, round, totalRounds int, proText, conText string, temperature float32) tea.Cmd {
	return func() tea.Msg {
		prompt := fmt.Sprintf(
			"你是一位公正严谨的辩论赛裁判。辩论主题：「%s」。\n\n"+
				"请对双方本轮发言进行点评，要求：\n"+
				"- 逐一核实双方引用的数据、事实、案例是否真实，若发现疑似伪造或无法验证的内容，明确指出\n"+
				"- 评价双方论据的逻辑严密性\n"+
				"- 指出双方论证中的漏洞或谬误\n"+
				"- 判断本轮哪方更有说服力，并简述理由\n"+
				"- 点评精炼，200字以内\n"+
				"- 禁止使用任何Markdown格式，直接输出纯文本\n\n"+
				"正方发言：\n%s\n\n"+
				"反方发言：\n%s",
			topic, proText, conText,
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
		for _, e := range entries {
			debateLog.WriteString(fmt.Sprintf("\n[%s] %s: %s", e.label, e.speaker, e.text))
		}

		prompt := fmt.Sprintf(
			"你是一位公正严谨的辩论赛裁判。辩论主题：「%s」。\n\n"+
				"请按以下模板输出总评（禁止使用Markdown格式，直接输出纯文本）：\n\n"+
				"【辩论总评】\n"+
				"主题：「%s」\n\n"+
				"一、正方核心论点\n"+
				"（用2-3句话概括正方的主要论点脉络）\n\n"+
				"二、反方核心论点\n"+
				"（用2-3句话概括反方的主要论点脉络）\n\n"+
				"三、关键分歧\n"+
				"（列出双方最核心的3-5个分歧点，用1-2句话描述每个分歧）\n\n"+
				"四、事实核查\n"+
				"（列出发现的疑似伪造数据或无法验证的事实，若无则写「未发现明显伪造」）\n\n"+
				"五、逻辑评价\n"+
				"（用1-2句话评价双方整体逻辑严密性）\n\n"+
				"六、最终判定\n"+
				"胜方：（正方/反方）\n"+
				"理由：（2-3句话说明判定理由）\n\n"+
				"七、改进建议\n"+
				"正方：（1句话）\n"+
				"反方：（1句话）\n\n"+
				"辩论记录：%s",
			topic, topic, debateLog.String(),
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

// ─── 导出功能 ────────────────────────────────────────

func exportMarkdown(m model) (string, error) {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("# AI 辩论报告\n\n"))
	b.WriteString(fmt.Sprintf("**主题**：%s\n\n", m.topic))
	b.WriteString(fmt.Sprintf("**模式**：%s\n\n", m.currentMode.name))
	b.WriteString(fmt.Sprintf("**生成时间**：%s\n\n", time.Now().Format("2006-01-02 15:04:05")))
	b.WriteString("---\n\n")

	b.WriteString("## 双方立场\n\n")
	b.WriteString(fmt.Sprintf("**正方立场**：%s\n\n", m.proStance))
	b.WriteString(fmt.Sprintf("**反方立场**：%s\n\n", m.conStance))
	b.WriteString("---\n\n")

	b.WriteString("## 论点图谱\n\n")

	// 收集各方论点
	var proPoints, conPoints []string
	for _, e := range m.entries {
		switch e.speaker {
		case "pro":
			proPoints = append(proPoints, fmt.Sprintf("- [%s] %s", e.label, e.text))
		case "con":
			conPoints = append(conPoints, fmt.Sprintf("- [%s] %s", e.label, e.text))
		}
	}

	b.WriteString("### 正方论点线\n\n")
	for _, p := range proPoints {
		b.WriteString(p)
		b.WriteString("\n\n")
	}

	b.WriteString("### 反方论点线\n\n")
	for _, p := range conPoints {
		b.WriteString(p)
		b.WriteString("\n\n")
	}

	b.WriteString("---\n\n")

	// 裁判点评
	b.WriteString("## 裁判点评\n\n")
	for _, e := range m.entries {
		if e.speaker == "judge" {
			b.WriteString(fmt.Sprintf("**%s**\n\n%s\n\n", e.label, e.text))
		}
	}
	b.WriteString("---\n\n")

	// 裁判总评（含关键分歧）
	b.WriteString("## 裁判总评与关键分歧\n\n")
	for _, e := range m.entries {
		if e.speaker == "judge_final" {
			b.WriteString(e.text)
			b.WriteString("\n\n")
		}
	}

	b.WriteString("---\n\n")
	b.WriteString("## 完整辩论记录\n\n")

	var roundNum int
	for _, e := range m.entries {
		switch e.speaker {
		case "pro":
			b.WriteString(fmt.Sprintf("### %s — 正方\n\n%s\n\n", e.label, e.text))
		case "con":
			b.WriteString(fmt.Sprintf("### %s — 反方\n\n%s\n\n", e.label, e.text))
		case "judge":
			roundNum++
			b.WriteString(fmt.Sprintf("### %s\n\n%s\n\n", e.label, e.text))
		case "judge_final":
			b.WriteString(fmt.Sprintf("### %s\n\n%s\n\n", e.label, e.text))
		}
	}

	return b.String(), nil
}

func exportPDF(m model, path string) error {
	pdf := gofpdf.New("P", "mm", "A4", "")
	pdf.SetAutoPageBreak(true, 15)

	pdf.AddPage()

	// 支持中文的字体（使用内置字体时用英文，或嵌入中文字体）
	// 这里使用 ASCII 兼容的标题，正文中的中文用简单的字体处理
	pdf.SetFont("Helvetica", "B", 18)
	pdf.CellFormat(190, 10, "AI Debate Report", "", 1, "C", false, 0, "")
	pdf.Ln(4)

	pdf.SetFont("Helvetica", "", 12)
	pdf.CellFormat(190, 8, fmt.Sprintf("Topic: %s", m.topic), "", 1, "L", false, 0, "")
	pdf.CellFormat(190, 8, fmt.Sprintf("Mode: %s", m.currentMode.name), "", 1, "L", false, 0, "")
	pdf.CellFormat(190, 8, fmt.Sprintf("Generated: %s", time.Now().Format("2006-01-02 15:04:05")), "", 1, "L", false, 0, "")
	pdf.Ln(4)

	// 分割线
	pdf.SetDrawColor(100, 100, 100)
	pdf.Line(10, pdf.GetY(), 200, pdf.GetY())
	pdf.Ln(4)

	// 立场
	pdf.SetFont("Helvetica", "B", 13)
	pdf.Cell(0, 8, "Stances")
	pdf.Ln(8)
	pdf.SetFont("Helvetica", "", 11)
	pdf.MultiCell(0, 6, fmt.Sprintf("[PRO] %s", truncateForPDF(m.proStance, 300)), "", "", false)
	pdf.Ln(2)
	pdf.MultiCell(0, 6, fmt.Sprintf("[CON] %s", truncateForPDF(m.conStance, 300)), "", "", false)
	pdf.Ln(6)

	// 裁判总评
	pdf.SetFont("Helvetica", "B", 13)
	pdf.Cell(0, 8, "Final Verdict & Key Disagreements")
	pdf.Ln(8)
	pdf.SetFont("Helvetica", "", 10)

	for _, e := range m.entries {
		if e.speaker == "judge_final" {
			lines := splitLines(e.text, 90)
			for _, line := range lines {
				pdf.MultiCell(0, 5, line, "", "", false)
			}
			pdf.Ln(4)
		}
	}

	// 辩论记录
	pdf.AddPage()
	pdf.SetFont("Helvetica", "B", 13)
	pdf.Cell(0, 8, "Full Debate Record")
	pdf.Ln(10)

	for _, e := range m.entries {
		switch e.speaker {
		case "pro":
			pdf.SetFont("Helvetica", "B", 10)
			pdf.Cell(0, 6, fmt.Sprintf("%s - PRO", e.label))
			pdf.Ln(7)
			pdf.SetFont("Helvetica", "", 9)
			for _, line := range splitLines(e.text, 95) {
				pdf.MultiCell(0, 4.5, line, "", "", false)
			}
			pdf.Ln(3)
		case "con":
			pdf.SetFont("Helvetica", "B", 10)
			pdf.Cell(0, 6, fmt.Sprintf("%s - CON", e.label))
			pdf.Ln(7)
			pdf.SetFont("Helvetica", "", 9)
			for _, line := range splitLines(e.text, 95) {
				pdf.MultiCell(0, 4.5, line, "", "", false)
			}
			pdf.Ln(3)
		case "judge":
			pdf.SetFont("Helvetica", "B", 10)
			pdf.Cell(0, 6, fmt.Sprintf("%s", e.label))
			pdf.Ln(7)
			pdf.SetFont("Helvetica", "", 9)
			for _, line := range splitLines(e.text, 95) {
				pdf.MultiCell(0, 4.5, line, "", "", false)
			}
			pdf.Ln(3)
		case "judge_final":
			pdf.SetFont("Helvetica", "B", 10)
			pdf.Cell(0, 6, "FINAL VERDICT")
			pdf.Ln(7)
			pdf.SetFont("Helvetica", "", 9)
			for _, line := range splitLines(e.text, 95) {
				pdf.MultiCell(0, 4.5, line, "", "", false)
			}
			pdf.Ln(3)
		}
	}

	return pdf.OutputFileAndClose(path)
}

func truncateForPDF(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

func splitLines(s string, maxLen int) []string {
	var lines []string
	isBreak := func(r rune) bool {
		return r == ' ' || r == ',' || r == '，' || r == '。' || r == '\n'
	}
	for _, para := range strings.Split(s, "\n") {
		para = strings.TrimSpace(para)
		if para == "" {
			lines = append(lines, "")
			continue
		}
		runes := []rune(para)
		for len(runes) > maxLen {
			splitAt := maxLen
			for splitAt > 0 && !isBreak(runes[splitAt]) {
				splitAt--
			}
			if splitAt == 0 {
				splitAt = maxLen
			}
			lines = append(lines, strings.TrimSpace(string(runes[:splitAt])))
			runes = runes[splitAt:]
		}
		lines = append(lines, strings.TrimSpace(string(runes)))
	}
	return lines
}

// ─── 工具函数 ────────────────────────────────────────

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func accentColorStr(s string) string {
	return lipgloss.NewStyle().Foreground(accentColor).Render(s)
}

func accentedStr(s string) string {
	return lipgloss.NewStyle().Foreground(accentColor).Bold(true).Render(s)
}

// ─── Markdown 清理 ───────────────────────────────────

var (
	reBold   = regexp.MustCompile(`\*\*(.+?)\*\*`)
	reItalic = regexp.MustCompile(`\*(.+?)\*`)
	reCode   = regexp.MustCompile("`(.+?)`")
	reHeader = regexp.MustCompile(`^#{1,6}\s+`)
	reLink   = regexp.MustCompile(`\[(.+?)\]\(.+?\)`)
	reList   = regexp.MustCompile(`^[\s]*[-+*]\s+`)
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
	// 检查 config.yml 是否存在
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
	m, runErr := p.Run()
	if runErr != nil {
		fmt.Printf("错误: %v\n", runErr)
		os.Exit(1)
	}

	// 检查是否需要导出
	finalModel := m.(model)
	if finalModel.phase == phaseDone {
		doExport(finalModel)
	}
}

func doExport(m model) {
	cwd, _ := os.Getwd()
	baseName := sanitizeFilename(m.topic)
	if baseName == "" {
		baseName = "debate"
	}

	mdPath := filepath.Join(cwd, baseName+".md")
	if err := os.WriteFile(mdPath, []byte(must(exportMarkdown(m))), 0644); err != nil {
		fmt.Printf("⚠ 导出 Markdown 失败: %v\n", err)
	} else {
		fmt.Printf("✓ 报告已导出: %s\n", mdPath)
	}

	pdfPath := filepath.Join(cwd, baseName+".pdf")
	if err := exportPDF(m, pdfPath); err != nil {
		fmt.Printf("⚠ 导出 PDF 失败: %v\n", err)
	} else {
		fmt.Printf("✓ 报告已导出: %s\n", pdfPath)
	}
}

func must(s string, err error) string {
	if err != nil {
		return ""
	}
	return s
}

func sanitizeFilename(name string) string {
	re := regexp.MustCompile(`[<>:"/\\|?*\s]+`)
	return re.ReplaceAllString(strings.TrimSpace(name), "_")
}