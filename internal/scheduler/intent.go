package scheduler

import (
	"fmt"
	"hash/fnv"
	"regexp"
	"strconv"
	"strings"
	"time"

	"goplay/internal/domain"
)

type IntentKind string

const (
	IntentConstraint IntentKind = "constraint"
	IntentPreference IntentKind = "preference"
	IntentQuestion   IntentKind = "question"
	IntentCommand    IntentKind = "command"
	IntentChat       IntentKind = "chat"
)

type Intent struct {
	Kind       IntentKind         `json:"kind"`
	MessageID  string             `json:"message_id"`
	Raw        string             `json:"raw"`
	Scope      string             `json:"scope"` // instance or global
	Constraint *domain.Constraint `json:"constraint,omitempty"`
	Preference *domain.Preference `json:"preference,omitempty"`
	Command    string             `json:"command,omitempty"`
	Confidence float64            `json:"confidence"`
	Reply      string             `json:"reply,omitempty"`
}

type ClassifyContext struct {
	InstanceID   string
	GameName     string
	KnownActions []string
	KnownTargets []string
	KnownTags    []string
	Now          time.Time
}

type Classifier interface {
	Classify(msg Message, cc ClassifyContext) Intent
}

// RuleClassifier is deterministic and runs inside the event boundary, so it
// adds no model latency. It can be paired with an LLM classifier later; the
// scheduler only needs the Intent.
type RuleClassifier struct{}

var (
	reTTLSeconds = regexp.MustCompile(`(\d+)\s*(秒|秒钟|seconds?|secs?)`)
	reTTLMinutes = regexp.MustCompile(`(\d+)\s*(分钟|minutes?|mins?)`)
	reQuestion   = regexp.MustCompile(`(\?|？|吗|什么|怎么|为什么|如何|哪|是否|what|how|why|which|can you|do you)\b?`)
)

func (RuleClassifier) Classify(msg Message, cc ClassifyContext) Intent {
	text := strings.TrimSpace(msg.Text)
	low := strings.ToLower(text)
	now := cc.Now
	if now.IsZero() {
		now = time.Now()
	}
	base := Intent{MessageID: msg.ID, Raw: text, Scope: "instance", Confidence: 0.55}

	// Explicit prefixes let the user override the heuristic.
	switch {
	case strings.HasPrefix(low, "/constraint") || strings.HasPrefix(text, "/约束"):
		text = strings.TrimSpace(text[strings.Index(text, " ")+1:])
		return constraintIntent(base, msg, text, cc, now, 0.99)
	case strings.HasPrefix(low, "/pref") || strings.HasPrefix(text, "/偏好"):
		text = strings.TrimSpace(text[strings.Index(text, " ")+1:])
		return preferenceIntent(base, msg, text, cc, now, 0.99)
	}

	// Control commands are not preferences.
	if cmd := parseCommand(low); cmd != "" {
		base.Kind = IntentCommand
		base.Command = cmd
		base.Confidence = 0.98
		base.Reply = commandReply(cmd)
		return base
	}

	// Preference language ("你太激进了", "稳一点") must win over the generic
	// "don't ..." constraint rule, otherwise style feedback becomes a hard ban.
	if p, ok := parsePreference(text, low, msg, now); ok {
		p.Confidence = 0.86
		return p
	}

	// Constraint language with a concrete action/target.
	if c, ok := parseConstraint(text, low, msg, cc, now); ok {
		return c
	}

	if reQuestion.MatchString(low) || strings.HasSuffix(text, "。") == false && strings.HasSuffix(text, "?") {
		base.Kind = IntentQuestion
		base.Confidence = 0.6
		return base
	}
	base.Kind = IntentChat
	base.Confidence = 0.4
	return base
}

func constraintIntent(base Intent, msg Message, text string, cc ClassifyContext, now time.Time, conf float64) Intent {
	base.Kind = IntentConstraint
	base.Confidence = conf
	base.Scope = "instance" // conversational constraints are hard, instance-scoped
	base.Constraint = buildConstraint(msg, text, cc, now)
	base.Reply = fmt.Sprintf("硬约束已记录（实例 %s）：%s。将在下一个决策周期生效。", cc.InstanceID, short(text, 80))
	return base
}

func preferenceIntent(base Intent, msg Message, text string, cc ClassifyContext, now time.Time, conf float64) Intent {
	base.Kind = IntentPreference
	base.Confidence = conf
	base.Scope = "global"
	if strings.Contains(text, "这个游戏") || strings.Contains(text, "这局") || strings.Contains(text, "本游戏") || strings.Contains(strings.ToLower(text), "this game") {
		base.Scope = "instance"
	}
	w, hints := preferenceWeights(text)
	if len(w) == 0 && len(hints) == 0 {
		hints = []string{text}
	}
	base.Preference = &domain.Preference{
		Text:      text,
		Weights:   w,
		AppliesTo: nil,
		Source:    msg.Source,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if len(hints) > 0 {
		base.Preference.Text = text + " | hint: " + strings.Join(hints, "; ")
	}
	base.Reply = "偏好已更新为软权重（不会中断当前动作序列）：" + short(text, 80)
	return base
}

func buildConstraint(msg Message, text string, cc ClassifyContext, now time.Time) *domain.Constraint {
	low := strings.ToLower(text)
	c := &domain.Constraint{
		Text:       text,
		Source:     msg.Source,
		InstanceID: cc.InstanceID,
		CreatedAt:  now,
		Active:     true,
	}
	verbs := map[string][]string{
		"打": {"attack"}, "攻击": {"attack"}, "揍": {"attack"}, "杀": {"attack", "kill"},
		"用": {"use"}, "使用": {"use"}, "释放": {"cast"}, "施放": {"cast"},
		"买": {"buy"}, "购买": {"buy"}, "卖": {"sell"},
		"去": {"move", "advance"}, "前往": {"move", "advance"}, "进入": {"enter"},
		"选": {"select"}, "选择": {"select"}, "点": {"click"}, "点击": {"click"},
		"attack": {"attack"}, "use": {"use"}, "buy": {"buy"}, "sell": {"sell"},
		"go": {"move", "advance"}, "click": {"click"}, "select": {"select"},
		"kill": {"attack", "kill"}, "cast": {"cast"},
	}
	for v, actions := range verbs {
		if strings.Contains(low, v) {
			for _, a := range actions {
				if !containsFold(c.DenyActions, a) {
					c.DenyActions = append(c.DenyActions, a)
				}
			}
		}
	}
	// Known actions mentioned literally (generic fallback).
	for _, a := range cc.KnownActions {
		if a != "" && strings.Contains(low, strings.ToLower(a)) && !containsFold(c.DenyActions, a) {
			c.DenyActions = append(c.DenyActions, a)
		}
	}
	// Targets: only accept nouns the active game actually exposes, so a chat
	// sentence cannot accidentally ban an unrelated action.
	for _, t := range cc.KnownTargets {
		if t == "" {
			continue
		}
		if strings.Contains(low, strings.ToLower(t)) {
			c.DenyTargets = append(c.DenyTargets, t)
		}
	}
	for _, t := range cc.KnownTags {
		if t != "" && strings.Contains(low, strings.ToLower(t)) {
			c.DenyTags = append(c.DenyTags, t)
		}
	}
	if len(c.DenyActions) == 0 && len(c.DenyTargets) == 0 && len(c.DenyTags) == 0 {
		// A negation with no recognizable action still becomes a scoped hard
		// constraint so the policy prompt sees it, but it denies nothing by
		// itself. The LLM classifier can refine it later.
		c.DenyTags = []string{"unspecified_forbidden"}
	}
	if d := parseTTL(low); d > 0 {
		t := now.Add(d)
		c.ExpiresAt = &t
	}
	h := fnv.New64a()
	fmt.Fprintf(h, "%s|%s|%d", cc.InstanceID, text, now.UnixNano())
	c.ID = fmt.Sprintf("c%x", h.Sum64())
	return c
}

func parseTTL(low string) time.Duration {
	if m := reTTLSeconds.FindStringSubmatch(low); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil {
			return time.Duration(n) * time.Second
		}
	}
	if m := reTTLMinutes.FindStringSubmatch(low); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil {
			return time.Duration(n) * time.Minute
		}
	}
	return 0
}

func parsePreference(text, low string, msg Message, now time.Time) (Intent, bool) {
	type rule struct {
		keys    []string
		weights map[string]float64
		hint    string
	}
	rules := []rule{
		{[]string{"太激进", "激进", "aggressive", "冲动", "冒进"}, map[string]float64{"aggression": -0.3, "risk_aversion": 0.25}, "play less aggressively"},
		{[]string{"保守", "稳一点", "稳妥", "cautious", "conservative", "稳"}, map[string]float64{"aggression": -0.2, "risk_aversion": 0.2}, "prefer cautious play"},
		{[]string{"多探索", "探索", "explore", "好奇心"}, map[string]float64{"exploration": 0.25}, "explore more"},
		{[]string{"别冒险", "安全第一", "avoid risk", "risk", "谨慎"}, map[string]float64{"risk_aversion": 0.35}, "avoid risky actions"},
		{[]string{"快一点", "快点", "speed", "faster", "效率"}, map[string]float64{"speed": 0.2}, "prefer faster decisions"},
		{[]string{"慢一点", "slow", "take your time"}, map[string]float64{"speed": -0.2}, "take more time"},
		{[]string{"省着", "节省", "节约", "save resource"}, map[string]float64{"resource_aversion": 0.3}, "conserve resources"},
	}
	for _, r := range rules {
		for _, k := range r.keys {
			if strings.Contains(low, strings.ToLower(k)) {
				base := Intent{MessageID: msg.ID, Raw: text, Scope: "global", Confidence: 0.8}
				return preferenceIntent(base, msg, text, ClassifyContext{Now: now}, now, 0.8), true
			}
		}
	}
	// Style / like / prefer statements.
	for _, k := range []string{"喜欢", "偏好", "风格", "prefer", "style", "like"} {
		if strings.Contains(low, k) {
			base := Intent{MessageID: msg.ID, Raw: text, Scope: "global", Confidence: 0.7}
			return preferenceIntent(base, msg, text, ClassifyContext{Now: now}, now, 0.7), true
		}
	}
	return Intent{}, false
}

func preferenceWeights(text string) (map[string]float64, []string) {
	low := strings.ToLower(text)
	w := map[string]float64{}
	var hints []string
	add := func(k string, v float64) { w[k] += v }
	if strings.Contains(low, "激进") || strings.Contains(low, "aggressive") || strings.Contains(low, "冲动") || strings.Contains(low, "冒进") {
		add("aggression", -0.3)
		add("risk_aversion", 0.25)
		hints = append(hints, "play less aggressively")
	}
	if strings.Contains(low, "保守") || strings.Contains(low, "稳") || strings.Contains(low, "cautious") || strings.Contains(low, "conservative") {
		add("aggression", -0.2)
		add("risk_aversion", 0.2)
		hints = append(hints, "prefer cautious play")
	}
	if strings.Contains(low, "探索") || strings.Contains(low, "explore") {
		add("exploration", 0.25)
		hints = append(hints, "explore more")
	}
	if strings.Contains(low, "冒险") || strings.Contains(low, "risk") || strings.Contains(low, "谨慎") || strings.Contains(low, "安全") {
		add("risk_aversion", 0.35)
		hints = append(hints, "avoid risky actions")
	}
	if strings.Contains(low, "省") || strings.Contains(low, "节约") || strings.Contains(low, "save") {
		add("resource_aversion", 0.3)
		hints = append(hints, "conserve resources")
	}
	if len(w) == 0 {
		return nil, nil
	}
	return w, hints
}

func parseConstraint(text, low string, msg Message, cc ClassifyContext, now time.Time) (Intent, bool) {
	neg := []string{"别", "不要", "不能", "禁止", "先不", "不准", "don't", "do not", "never", "avoid", "stop"}
	hasNeg := false
	for _, n := range neg {
		if strings.Contains(low, n) {
			hasNeg = true
			break
		}
	}
	if !hasNeg {
		return Intent{}, false
	}
	// A negation must reference something actionable.
	actionish := false
	for _, a := range []string{"打", "攻击", "用", "买", "卖", "去", "进入", "选", "点", "释放", "施放", "attack", "use", "buy", "sell", "go", "click", "select", "cast", "kill"} {
		if strings.Contains(low, a) {
			actionish = true
			break
		}
	}
	if !actionish {
		for _, t := range cc.KnownTargets {
			if t != "" && strings.Contains(low, strings.ToLower(t)) {
				actionish = true
				break
			}
		}
	}
	if !actionish {
		return Intent{}, false
	}
	base := Intent{MessageID: msg.ID, Raw: text, Scope: "instance", Confidence: 0.9}
	return constraintIntent(base, msg, text, cc, now, 0.9), true
}

func parseCommand(low string) string {
	low = strings.TrimSpace(low)
	switch {
	case low == "暂停" || low == "pause" || low == "停一下":
		return "pause"
	case low == "继续" || low == "resume" || low == "继续玩" || low == "接着玩":
		return "resume"
	case low == "停止" || low == "stop" || low == "结束" || low == "quit":
		return "stop"
	}
	return ""
}

func commandReply(cmd string) string {
	switch cmd {
	case "pause":
		return "已暂停自动游玩。"
	case "resume":
		return "已恢复自动游玩。"
	case "stop":
		return "已停止该游戏实例。"
	}
	return "命令已接收。"
}

func short(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
}

func containsFold(list []string, v string) bool {
	for _, s := range list {
		if strings.EqualFold(s, v) {
			return true
		}
	}
	return false
}
