package aichat

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/nichuanfang/gymdl/config"
	"github.com/nichuanfang/gymdl/utils"
	openai "github.com/sashabaranov/go-openai"
)

const (
	historyMessageLimit = 40
	historyRowLimit     = 80
	candidateTTL        = 30 * time.Minute
)

// Engine owns one configured conversation turn. Tools are injected by the bot
// layer so this package never reaches into Telegram, downloads, or config files.
type Engine struct {
	cfg     *config.AIConfig
	client  *openai.Client
	store   *Store
	history HistorySource
	skills  SkillLister
	tools   []Tool
	owned   OwnedFunc
}

func NewEngine(cfg *config.AIConfig, store *Store, history HistorySource, skills SkillLister, tools []Tool, owned OwnedFunc) (*Engine, error) {
	if cfg == nil || strings.TrimSpace(cfg.ApiKey) == "" || strings.TrimSpace(cfg.Model) == "" {
		return nil, fmt.Errorf("AI 配置不完整")
	}
	clientCfg := openai.DefaultConfig(cfg.ApiKey)
	if cfg.BaseUrl != "" {
		clientCfg.BaseURL = strings.TrimRight(cfg.BaseUrl, "/")
	}
	return &Engine{
		cfg:     cfg,
		client:  openai.NewClientWithConfig(clientCfg),
		store:   store,
		history: history,
		skills:  skills,
		tools:   tools,
		owned:   owned,
	}, nil
}

// turnTimeout is the budget for one user turn including all tool rounds. The
// per-request timeout stays separate so a single slow call can fail without
// letting a turn run unbounded.
func (e *Engine) turnTimeout() time.Duration {
	request := time.Duration(e.cfg.RequestTimeoutSeconds) * time.Second
	if request <= 0 {
		request = 90 * time.Second
	}
	budget := time.Duration(e.effectiveRounds()+1) * request * 3 / 2
	if budget < 60*time.Second {
		budget = 60 * time.Second
	}
	if budget > 10*time.Minute {
		budget = 10 * time.Minute
	}
	return budget
}

// effectiveRounds is the tool-round budget actually used for this turn. The
// prompt asks for 1-3 named tracks with one lookup each plus the final answer,
// so a configured 4 cannot cover a normal recommendation turn; the floor is
// raised to 6. A larger configured value is always respected. Both the loop and
// turnTimeout use this, so they cannot disagree.
func (e *Engine) effectiveRounds() int {
	rounds := e.cfg.MaxToolRounds
	if rounds < 6 {
		rounds = 6
	}
	return rounds
}

// TurnInput carries the scope and the user's message for one agent turn.
type TurnInput struct {
	UserID   int64
	ChatID   int64
	Text     string
	ForceNew bool
}

// TurnOutput is what the Telegram layer renders.
type TurnOutput struct {
	Reply      string
	Candidates []Candidate
	ToolNote   string
}

// Respond runs the tool-calling loop and returns a grounded answer.
func (e *Engine) Respond(ctx context.Context, in TurnInput) (*TurnOutput, error) {
	if strings.TrimSpace(in.Text) == "" {
		return &TurnOutput{Reply: "想听点什么？直接告诉我心情、场景或歌手。"}, nil
	}
	if in.ForceNew {
		if err := e.store.Reset(in.UserID, in.ChatID); err != nil {
			return nil, err
		}
	}
	// Bound the whole turn, not just a single provider call: a tool-heavy turn
	// used to be able to hold the chat for several minutes on sequential
	// request timeouts.
	if deadline := e.turnTimeout(); deadline > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, deadline)
		defer cancel()
	}
	history, err := e.store.History(in.UserID, in.ChatID, historyMessageLimit)
	if err != nil {
		return nil, err
	}
	profile, profileErr := e.history.Load(ctx)
	collector := CollectorFrom(ctx)
	messages := []openai.ChatCompletionMessage{{
		Role:    openai.ChatMessageRoleSystem,
		Content: e.systemPrompt(profile) + e.routedSkillInstructions(ctx, in.Text),
	}}
	for _, m := range history {
		role := m.Role
		if role != openai.ChatMessageRoleUser && role != openai.ChatMessageRoleAssistant {
			continue
		}
		messages = append(messages, openai.ChatCompletionMessage{Role: role, Content: m.Content})
	}
	messages = append(messages, openai.ChatCompletionMessage{
		Role:    openai.ChatMessageRoleUser,
		Content: in.Text,
	})

	timeout := time.Duration(e.cfg.RequestTimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 90 * time.Second
	}
	rounds := e.effectiveRounds()
	ReportProgress(ctx, "正在理解你的需求…")
	toolDefs := e.toolDefinitions()
	var usedTools []string
	var final string
	for round := 0; round <= rounds; round++ {
		req := openai.ChatCompletionRequest{
			Model:       e.cfg.Model,
			Messages:    messages,
			Temperature: 0.4,
			MaxTokens:   1200,
		}
		if len(toolDefs) > 0 && round < rounds {
			req.Tools = toolDefs
			req.ToolChoice = "auto"
		}
		reqCtx, cancel := context.WithTimeout(ctx, timeout)
		resp, err := e.client.CreateChatCompletion(reqCtx, req)
		cancel()
		if err != nil {
			// Persist the failed request next to the log so provider-side schema
			// rejections are diagnosable without reproducing them by hand.
			dumpFailedRequest(req, err)
			if ctx.Err() != nil {
				return nil, fmt.Errorf("这一轮处理超时了（%s），已经停止。可以说得更具体些，或直接发歌曲链接。",
					e.turnTimeout().Round(time.Second))
			}
			if profileErr != nil {
				return nil, fmt.Errorf("AI 请求失败: %v（收听历史也不可用: %v）", err, profileErr)
			}
			return nil, fmt.Errorf("AI 请求失败: %w", err)
		}
		if len(resp.Choices) == 0 {
			return nil, fmt.Errorf("AI 返回为空")
		}
		choice := resp.Choices[0].Message
		if len(choice.ToolCalls) == 0 {
			final = strings.TrimSpace(choice.Content)
			break
		}
		messages = append(messages, openai.ChatCompletionMessage{
			Role:      openai.ChatMessageRoleAssistant,
			ToolCalls: choice.ToolCalls,
			Content:   choice.Content,
		})
		for _, call := range choice.ToolCalls {
			ReportProgress(ctx, progressLabel(call.Function.Name))
			result := e.runTool(ctx, call.Function.Name, call.Function.Arguments, &usedTools)
			// Some gateways reject tool messages with blank content, and an
			// empty payload also gives the model nothing to reason about.
			if strings.TrimSpace(result) == "" {
				result = `{"result":"empty"}`
			}
			messages = append(messages, openai.ChatCompletionMessage{
				Role:       openai.ChatMessageRoleTool,
				ToolCallID: call.ID,
				Content:    result,
			})
		}
	}
	if final == "" {
		final = "我查到了结果，但没能生成完整回答。可以再具体一点，或直接发歌曲链接。"
	}
	if err := e.store.AppendMessages(in.UserID, in.ChatID, []Message{
		{Role: openai.ChatMessageRoleUser, Content: in.Text},
		{Role: openai.ChatMessageRoleAssistant, Content: final},
	}, historyRowLimit); err != nil {
		return nil, err
	}
	candidates, err := e.candidates(ctx, in, collector, final)
	if err != nil {
		return nil, err
	}
	return &TurnOutput{Reply: final, Candidates: candidates, ToolNote: strings.Join(unique(usedTools), ", ")}, nil
}

// candidates converts grounded search hits into stable button targets. Results
// are capped so a busy search cannot produce an unrenderable message.
func (e *Engine) candidates(ctx context.Context, in TurnInput, collector *Collector, answer string) ([]Candidate, error) {
	if collector == nil {
		return nil, nil
	}
	limit := e.cfg.MaxSuggestions
	if limit <= 0 {
		limit = 10
	}
	results := collector.Results()
	// Only surface tracks the model actually presented. Collector holds every
	// search hit, including failed attempts and near-misses, so showing all of
	// them made the button list disagree with the recommendation above it.
	results = filterMentioned(results, answer)
	out := make([]Candidate, 0, min(limit, len(results)))
	seenTracks := make(map[string]bool, len(results))
	for _, item := range results {
		if len(out) >= limit {
			break
		}
		// The same song often comes back from several providers and as several
		// uploads; keep one button per normalised title so the list stays
		// readable. Artist text varies wildly across platforms, so it is not
		// part of the key.
		trackKey := normalizeTrackKey(item.Name)
		if trackKey == "" || seenTracks[trackKey] {
			continue
		}
		seenTracks[trackKey] = true
		token := strings.ReplaceAll(uuid.NewString(), "-", "")
		candidate := Candidate{
			Token: token, Title: item.Name, Artist: item.Artists,
			Platform: item.Platform, URL: item.URL, Name: item.Name,
			Grounded: true, IsVIP: item.IsVIP, ExpiresAt: time.Now().Add(candidateTTL),
		}
		if e.owned != nil {
			owned, err := e.owned(ctx, item.Name, item.Artists, item.URL)
			if err == nil {
				candidate.Owned = owned
			}
		}
		out = append(out, candidate)
	}
	if err := e.store.SaveCandidates(in.UserID, in.ChatID, out); err != nil {
		return nil, err
	}
	return out, nil
}

// filterMentioned keeps hits whose title appears in the model's final answer.
// When the model named nothing usable we fall back to the full list so the user
// is never left without a way to act.
func filterMentioned(results []SearchResult, answer string) []SearchResult {
	if strings.TrimSpace(normalizeTrackKey(answer)) == "" {
		return results
	}
	matched := make([]SearchResult, 0, len(results))
	for _, item := range results {
		if mentionsTrack(answer, item.Name) {
			matched = append(matched, item)
		}
	}
	if len(matched) == 0 {
		// When the model clearly did list tracks (《》 or numbered lines) but
		// none of the collected hits match, the hits are near-misses from a
		// failed lookup. Showing them produced buttons unrelated to the answer
		// ("深夜放松" surfaced an unrelated upload). Only fall back to the full
		// list for a pure-prose answer that named no track at all.
		if answerNamesAnyTrack(answer) {
			return nil
		}
		return results
	}
	return matched
}

func (e *Engine) systemPrompt(profile *Profile) string {
	var b strings.Builder
	b.WriteString(e.cfg.SystemPrompt)
	if b.Len() == 0 {
		b.WriteString("你是 GymDL 的音乐助手，用中文简洁回答。")
	}
	b.WriteString("\n\n## 工作方式")
	b.WriteString("\n1. 先判断意图，再决定动作：找歌/找相似/查曲库/查歌词/排错/整理歌单/纯聊天。不要默认先搜索。")
	b.WriteString("\n2. 能力彼此平级，没有固定先后顺序。工具用于拿事实，技能用于拿判断规则，MCP 用于接外部服务。")
	b.WriteString("\n3. 需要事实时先取事实再回答：歌曲必须来自 search_music 的真实结果，不要凭记忆编链接、编曲目、编是否存在。")
	b.WriteString("\n4. 用户发来分享链接时，系统会直接下载，不会进入这段对话；不要要求用户先退出 AI 模式。")
	b.WriteString("\n5. 输出要能直接用：先给结论，再给必要细节；不复述工具原始输出，不暴露内部 URL、ID、绝对路径或密钥。推荐歌曲时用列表写出「歌名 — 艺人」。")
	b.WriteString("\n6. 不确定就说不确定。宁可少给两首，也不要凑数或推荐未验证的曲目。")
	b.WriteString("\n7. 多轮对话中沿用已确认的偏好；用户改口时以最新说法为准。")
	b.WriteString("\n8. 被问到“你有什么技能/工具”时，只列实际可用的技能名和一句话用途，不要展开成教程，也不要说自己有未列出的能力。")
	b.WriteString("\n\n## 边界")
	b.WriteString("\n- 你能帮用户下载：推荐结果会带「下载」按钮，用户点一下就会真的入库。所以不要说“我不能下载”，要引导用户点按钮；你只能说“在你点之前还没开始下载”。")
	b.WriteString("\n- 删除、改配置这类操作只给建议或等待用户确认，不声称已经执行。")
	b.WriteString("\n- 不编造歌名、歌词、专辑、发行年份或平台可下载性。")
	b.WriteString("\n- 只要涉及“有没有/能不能下载/推荐可播的曲目”，答案里的每首歌都必须来自本轮 search_music 的结果；工具报错、没找到、或你还没搜时，直接说明“暂时查不到”，不要凭记忆列出歌名或链接。")
	b.WriteString("\n- 用户明确排除的语言、场景、艺人不要再次推荐。")
	if e.skills != nil {
		if skills := e.skills.List(); len(skills) > 0 {
			b.WriteString("\n\n## 可用音乐技能")
			b.WriteString("\n命中的技能规则会直接附在本轮提示里；下面未命中的技能，需要时再调用 load_skill 载入。闲聊不必载入。")
			b.WriteString("\n涉及曲库数量、是否已下载、歌词、下载失败、批量下载、歌单分类、标签整理时，只靠工具不够，必须配合对应技能。")
			for _, s := range skills {
				b.WriteString("\n- " + s.Name + "：" + s.Description)
			}
		}
	}
	if profile != nil && profile.Scrobbles > 0 {
		b.WriteString(fmt.Sprintf("\n\n## 收听画像（最近 %d 天，共 %d 次）", profile.WindowDays, profile.Scrobbles))
		b.WriteString("\n常听艺人：" + strings.Join(profile.Artists, "、"))
		b.WriteString("\n常听曲目：" + strings.Join(profile.Tracks, "；"))
		b.WriteString("\n常听专辑：" + strings.Join(profile.Albums, "、"))
		b.WriteString("\n画像只用于排序偏好，不要因为画像里没有某首歌就拒绝推荐。")
	}
	return b.String()
}

// skillLoader is the optional capability the engine uses to inline a skill's
// instructions. When available, routing a skill no longer costs a model round
// trip and no longer depends on the model deciding to call load_skill.
type skillLoader interface {
	Instructions(name string) (string, bool)
}

// maxInlinedSkillBytes keeps a multi-skill turn from crowding out the
// conversation. Shipped skills are ~1-2KB each.
const maxInlinedSkillBytes = 12 << 10

// routedSkillInstructions inlines the skills that the deterministic trigger
// router matched. The prompt still tells the model about load_skill so it can
// pull any other skill it judges relevant.
func (e *Engine) routedSkillInstructions(ctx context.Context, text string) string {
	names := RouteSkills(text, e.mountedSkills())
	if len(names) == 0 {
		return ""
	}
	loader, ok := e.skills.(skillLoader)
	if !ok {
		return SkillHint(names)
	}
	var b strings.Builder
	total := 0
	for _, name := range names {
		content, found := loader.Instructions(name)
		if !found || strings.TrimSpace(content) == "" {
			continue
		}
		if total+len(content) > maxInlinedSkillBytes {
			break
		}
		total += len(content)
		b.WriteString("\n\n### 技能：" + name + "\n")
		b.WriteString(strings.TrimSpace(content))
		if collector := CollectorFrom(ctx); collector != nil {
			collector.AddSkill(name)
		}
	}
	if b.Len() == 0 {
		return SkillHint(names)
	}
	b.WriteString("\n\n以上技能已直接载入，按其中规则作答，不需要再调用 load_skill。")
	return b.String()
}

func (e *Engine) toolDefinitions() []openai.Tool {
	out := make([]openai.Tool, 0, len(e.tools))
	for _, tool := range e.tools {
		out = append(out, openai.Tool{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        tool.Name,
				Description: tool.Description,
				Parameters:  tool.Parameters,
			},
		})
	}
	return out
}

func (e *Engine) mountedSkills() []string {
	if e.skills == nil {
		return nil
	}
	skills := e.skills.List()
	names := make([]string, 0, len(skills))
	for _, skill := range skills {
		names = append(names, skill.Name)
	}
	return names
}

func (e *Engine) runTool(ctx context.Context, name, rawArgs string, used *[]string) string {
	*used = append(*used, name)
	var args map[string]any
	if strings.TrimSpace(rawArgs) != "" {
		if err := json.Unmarshal([]byte(rawArgs), &args); err != nil {
			return `{"error":"参数不是合法 JSON"}`
		}
	}
	if args == nil {
		args = map[string]any{}
	}
	for _, tool := range e.tools {
		if tool.Name != name {
			continue
		}
		result, err := tool.Run(ctx, args)
		if err != nil {
			return fmt.Sprintf(`{"error":%q}`, err.Error())
		}
		return result
	}
	return `{"error":"未知工具"}`
}

// progressLabel maps a tool name to a short stage the user can understand
// without knowing the tool's implementation.
func progressLabel(tool string) string {
	switch tool {
	case "search_music":
		return "正在检索真实曲目…"
	case "library_stats":
		return "正在统计本地曲库…"
	case "library_lookup":
		return "正在查本地曲库…"
	case "list_active_downloads":
		return "正在查看下载任务…"
	case "load_skill":
		return "正在整理回答规则…"
	}
	if strings.HasPrefix(tool, "mcp_") {
		return "正在调用外部服务…"
	}
	return "正在处理…"
}

func unique(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, v := range values {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

// trackDecorationWords are attribute words that do not change the identity of
// a track. They are stripped only as whole trailing words or inside brackets,
// so a title such as "Alive" can never be truncated into "a".
var trackDecorationWords = map[string]bool{
	"feat": true, "ft": true, "remaster": true, "remastered": true,
	"live": true, "version": true, "remix": true, "edit": true, "demo": true,
	"mono": true, "stereo": true, "instrumental": true, "acoustic": true,
	"版": true, "现场": true, "现场版": true, "伴奏": true, "完整版": true,
}

// stripAttributeGroups drops bracketed attribute groups such as "(feat. X)",
// "[Remastered]" or "（Live）", but keeps the text of groups that carry part of
// the title, such as "Luv(sic)". 《》 and 【】 wrap the title itself, so only the
// bracket characters are removed.
func stripAttributeGroups(key string) string {
	for _, pair := range [][2]string{{"(", ")"}, {"（", "）"}, {"[", "]"}} {
		open, close := pair[0], pair[1]
		for {
			start := strings.Index(key, open)
			if start < 0 {
				break
			}
			rel := strings.Index(key[start+len(open):], close)
			if rel < 0 {
				break
			}
			innerEnd := start + len(open) + rel
			inner := key[start+len(open) : innerEnd]
			rest := key[innerEnd+len(close):]
			if hasDecorationWord(inner) {
				key = key[:start] + " " + rest
			} else {
				key = key[:start] + " " + inner + " " + rest
			}
		}
	}
	return strings.NewReplacer("《", "", "》", "", "【", "", "】", "").Replace(key)
}

// stripDashDecorations drops a trailing "- Remastered", "— Live 2011" style
// suffix. The cut only happens when the text after the dash is purely a
// decoration, so hyphenated artist/music names such as "Jay-Z" stay intact.
func stripDashDecorations(key string) string {
	for _, dash := range []string{" - ", " – ", " — ", "-", "–", "—"} {
		if index := strings.LastIndex(key, dash); index >= 0 {
			tail := key[index+len(dash):]
			if strings.TrimSpace(tail) == "" || !hasDecorationWord(tail) {
				continue
			}
			return strings.TrimSpace(key[:index])
		}
	}
	return key
}

func hasDecorationWord(value string) bool {
	for _, field := range strings.FieldsFunc(value, func(r rune) bool {
		return !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') && r < 0x4e00
	}) {
		if trackDecorationWords[field] {
			return true
		}
	}
	// Chinese attribute groups are not word-delimited; check containment.
	for _, word := range []string{"版", "现场", "伴奏", "重制", "翻唱"} {
		if strings.Contains(value, word) {
			return true
		}
	}
	return false
}

// normalizeTrackKey collapses the decorations providers add around the same
// song: 《》 brackets, "(feat. X)", "- Remastered", full-width punctuation and
// case. Two uploads of one track then share a key and only one button remains.
// Spaces are preserved so short Latin titles keep a word boundary.
func normalizeTrackKey(name string) string {
	key := strings.ToLower(strings.TrimSpace(name))
	key = stripAttributeGroups(key)
	key = stripDashDecorations(key)
	key = strings.Map(func(r rune) rune {
		switch r {
		case '\'', '’', '`', '"', '·', '・', '.', ',', '，', '。', '!', '?', '？', '：', ':', '、', '*':
			return -1
		case '-', '—', '–', '_', '/', '|', '｜':
			return ' '
		}
		return r
	}, key)
	fields := strings.Fields(key)
	for len(fields) > 1 && trackDecorationWords[fields[len(fields)-1]] {
		fields = fields[:len(fields)-1]
	}
	// A bare year or version number left behind by a stripped marker
	// ("remastered 2011") is not part of the title.
	trimmed := fields
	for len(trimmed) > 1 && isDigits(trimmed[len(trimmed)-1]) {
		if trackDecorationWords[trimmed[len(trimmed)-2]] {
			trimmed = trimmed[:len(trimmed)-1]
			continue
		}
		break
	}
	return strings.Join(trimmed, " ")
}

// mentionsTrack reports whether the model presented a title as a track rather
// than merely using the same word in prose. A plain substring test is too
// loose: an answer to "适合傍晚通勤听的歌" contains 傍晚 and 通勤, which used to
// turn the unrelated songs 《傍晚》 and 《通勤》 into download buttons.
//
// A title counts when it is wrapped in 《》, leads a list item, or is followed
// by an explicit artist separator. Short Latin keys additionally require a
// whole word so "A" never matches inside "Alive".
func mentionsTrack(answerRaw, rawTitle string) bool {
	title := strings.TrimSpace(rawTitle)
	if title == "" {
		return false
	}
	titleKey := normalizeTrackKey(title)
	if titleKey == "" {
		return false
	}
	if strings.Contains(answerRaw, "《"+title+"》") {
		return true
	}
	if utf8.RuneCountInString(titleKey) < 3 && isASCII(titleKey) {
		for _, field := range strings.FieldsFunc(normalizeTrackKey(answerRaw), func(r rune) bool {
			return !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9')
		}) {
			if field == titleKey {
				return true
			}
		}
		return false
	}
	for _, raw := range strings.Split(answerRaw, "\n") {
		line, isListItem := trimMarkdownPrefix(raw)
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, title) {
			// Tolerate provider decorations the model copied verbatim, e.g.
			// "《晴天 (Live)》 - 周杰伦".
			key := normalizeTrackKey(line)
			if !strings.HasPrefix(key, titleKey) {
				continue
			}
			rest := strings.TrimSpace(key[len(titleKey):])
			if isListItem && (rest == "" || startsLikeAttribute(rest)) {
				return true
			}
			continue
		}
		rest := strings.TrimSpace(line[len(title):])
		if isListItem || rest == "" || startsLikeAttribute(rest) {
			return true
		}
	}
	// Inline mentions such as "推荐 晴天（周杰伦）" or "想听 晴天 — 周杰伦":
	// the title must be followed by an explicit artist separator and preceded
	// only by a short lead-in, so prose like "适合傍晚通勤听的歌" is rejected.
	for _, raw := range strings.Split(answerRaw, "\n") {
		line, _ := trimMarkdownPrefix(raw)
		for offset := 0; ; {
			index := strings.Index(line[offset:], title)
			if index < 0 {
				break
			}
			index += offset
			rawBefore := line[:index]
			before := strings.TrimSpace(rawBefore)
			after := strings.TrimSpace(line[index+len(title):])
			offset = index + len(title)
			if utf8.RuneCountInString(before) > 8 {
				continue
			}
			// The title must start a segment: either the line begins with it or
			// it is preceded by whitespace/punctuation. "城市傍晚" therefore
			// never counts as a mention of the shorter song 《傍晚》.
			if before != "" && !strings.HasSuffix(rawBefore, " ") &&
				!strings.HasSuffix(rawBefore, "【") && !strings.HasSuffix(rawBefore, "「") {
				if last, _ := utf8.DecodeLastRuneInString(before); isWordRune(last) {
					continue
				}
			}
			// The title must not continue into a longer name
			// ("傍晚" inside "城市傍晚").
			if after != "" && !startsLikeAttribute(after) {
				continue
			}
			for _, sep := range []string{"（", "(", "—", "–", "-", "："} {
				if strings.HasPrefix(after, sep) {
					return true
				}
			}
		}
	}
	return false
}

// answerNamesAnyTrack reports whether the model formatted any line as a track
// entry, regardless of whether it came from the collector.
func answerNamesAnyTrack(answer string) bool {
	for _, raw := range strings.Split(answer, "\n") {
		line, isListItem := trimMarkdownPrefix(raw)
		if line == "" {
			continue
		}
		if isListItem {
			return true
		}
		if strings.Contains(line, "《") && strings.Contains(line, "》") {
			return true
		}
	}
	return false
}

// trimMarkdownPrefix removes list markers and emphasis from one answer line and
// reports whether the line was presented as a list item.
func trimMarkdownPrefix(raw string) (string, bool) {
	line := strings.TrimSpace(raw)
	line = strings.TrimLeft(line, "> ")
	isListItem := false
	if index := strings.IndexAny(line, ".)、）"); index >= 0 && index <= 2 && isDigits(strings.TrimSpace(line[:index])) {
		line = strings.TrimSpace(line[index+1:])
		isListItem = true
	} else if strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "* ") || strings.HasPrefix(line, "• ") {
		line = strings.TrimSpace(line[2:])
		isListItem = true
	}
	line = strings.Trim(line, "* ")
	return line, isListItem
}

// startsLikeAttribute reports whether the text after a title begins an artist
// or description segment rather than continuing the title word by word.
func startsLikeAttribute(rest string) bool {
	if rest == "" {
		return true
	}
	for _, sep := range []string{"—", "–", "-", "(", "（", "《", "|", ":", "·"} {
		if strings.HasPrefix(rest, sep) {
			return true
		}
	}
	// "城市傍晚 毛不易": whitespace separates the title from the artist.
	// normalizeTrackKey keeps single spaces, so "城市傍晚" never matches a
	// longer title such as "城市傍晚好" or a prose fragment.
	return strings.Contains(rest, " ")
}

// isWordRune reports whether r continues a word, so a title match must not
// start in the middle of one.
func isWordRune(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return true
	case r >= 0x4e00 && r <= 0x9fff:
		return true
	}
	return false
}

func isDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func isASCII(value string) bool {
	for _, r := range value {
		if r > 0x7f {
			return false
		}
	}
	return true
}

// dumpFailedRequest writes the exact outbound payload of a failed call so a
// provider 400 can be diagnosed from disk instead of guessed at. It is capped
// and best-effort: diagnostics must never turn a reply into an error.
func dumpFailedRequest(req openai.ChatCompletionRequest, cause error) {
	payload, err := json.Marshal(req.Messages)
	if err != nil {
		return
	}
	dir := filepath.Join("data", "logs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	path := filepath.Join(dir, "ai-request-failed.json")
	entry := map[string]any{
		"at":       time.Now().Format(time.RFC3339),
		"error":    utils.TruncateString(cause.Error(), 500),
		"model":    req.Model,
		"messages": json.RawMessage(payload),
	}
	encoded, err := json.MarshalIndent(entry, "", "  ")
	if err != nil || len(encoded) > 200<<10 {
		return
	}
	_ = os.WriteFile(path, encoded, 0o600)
}
