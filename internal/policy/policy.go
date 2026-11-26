// Package policy turns a state plus the game's legal actions into one action.
// It combines a contextual UCB bandit (cheap, always available) with an
// optional LLM policy (for unfamiliar situations), then runs every choice
// through a validator. Constraints and preferences arrive pre-merged in
// domain.Control: hard constraints only deny; preferences only shift scores.
package policy

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"strings"
	"sync"
	"time"

	"goplay/internal/domain"
	"goplay/internal/llm"
	"goplay/internal/metrics"
)

type Config struct {
	UCB      float64
	Prior    float64
	LLMEvery int
	MaxRisk  float64
	Seed     int64
	MaxCands int
	// RepeatLimit is how many identical decisions in a row are tolerated before
	// the policy deliberately tries a different legal action. A single repeated
	// key is cheap to try once and poisonous when it becomes a loop.
	RepeatLimit int
}

func DefaultConfig() Config {
	return Config{UCB: 0.9, Prior: 0.4, LLMEvery: 12, MaxRisk: 0.9, Seed: 1, MaxCands: 24, RepeatLimit: 3}
}

type Candidate struct {
	Action domain.Action
	Score  float64
	Mean   float64
	Visits int
	Note   string
}

type Decision struct {
	Action     domain.Action
	Score      float64
	Source     string // heuristic, llm, fallback
	StateKey   string
	ActionKey  string
	Candidates []Candidate
	Reason     string
	Provider   string
	Model      string
	Fallback   bool
	LatencyMS  float64
	LLMUsed    bool
	Demo       bool
	// Blocked lists risky actions (purchase/recharge controls, or anything that
	// is not a safe escape while a payment screen is open) that the hard filter
	// removed before scoring. It makes the safety veto visible in the trace.
	Blocked []string
}

type Input struct {
	State    domain.State
	Actions  []domain.Action
	Control  domain.Control
	Rules    domain.InstanceRules
	Hints    []string
	ForceLLM bool
	// GameName/Package and GamePrior are context for the LLM. The prior is a
	// soft hypothesis (genre, goals, what to watch), never a hard-coded key
	// script: the legal actions still come from the game profile.
	GameName    string
	GamePackage string
	GamePrior   string
	// Demo carries actions learned from a human demonstration. They are only
	// hints: the policy still checks that the action is in the current legal set.
	Demo       []domain.Action
	DemoReason string
	// Directive is a one-shot user instruction ("press Z"). It outranks learned
	// demonstrations and UCB, but still has to be in the current legal set and
	// still passes the validator: a typed instruction is not a safety bypass.
	Directive       []domain.Action
	DirectiveReason string
}

type arm struct {
	visits int
	mean   float64
}

type Validator struct {
	AllowedInput map[string]bool
	MaxRisk      float64
}

func NewValidator(maxRisk float64) Validator {
	return Validator{
		AllowedInput: map[string]bool{
			"key": true, "click": true, "tap": true, "swipe": true, "back": true,
			"text": true, "wait": true, "gamepad": true, "touch": true,
		},
		MaxRisk: maxRisk,
	}
}

func (v Validator) Validate(a domain.Action, legal []domain.Action) error {
	if a.Kind == "" {
		return fmt.Errorf("empty action kind")
	}
	found := false
	for _, l := range legal {
		if l.Key() == a.Key() {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("action %q is not in the current legal set", a.Key())
	}
	if a.Input != nil && a.Input.Type != "" && !v.AllowedInput[a.Input.Type] {
		return fmt.Errorf("input type %q is not allowlisted", a.Input.Type)
	}
	if v.MaxRisk > 0 && a.Risk > v.MaxRisk {
		return fmt.Errorf("risk %.2f exceeds cap %.2f", a.Risk, v.MaxRisk)
	}
	if a.Input != nil && (a.Input.Type == "click" || a.Input.Type == "tap") {
		if a.Input.X < 0 || a.Input.X > 1 || a.Input.Y < 0 || a.Input.Y > 1 {
			return fmt.Errorf("normalized click coordinates out of range: %.3f,%.3f", a.Input.X, a.Input.Y)
		}
	}
	return nil
}

type Policy struct {
	cfg   Config
	llm   llm.Provider
	reg   *metrics.Registry
	val   Validator
	mu    sync.Mutex
	q     map[string]map[string]*arm
	total int
	rng   *rand.Rand
	stats PolicyStats

	// idleStreak counts consecutive wait/back decisions. A screen-reading agent
	// that keeps choosing "wait" looks alive but never touches the game, so after
	// a couple of passive choices the next decision must try an active action.
	idleStreak int

	// lastActionKey/lastActionStreak detect a loop of the same key. recent keeps
	// the last few recorded outcomes so the LLM prompt can see what just happened
	// instead of guessing from a static state summary.
	lastActionKey    string
	lastActionStreak int
	lastReward       float64
	recent           []string
}

type PolicyStats struct {
	Decisions    int64  `json:"decisions"`
	LLMCalls     int64  `json:"llm_calls"`
	LLMFails     int64  `json:"llm_fails"`
	Fallbacks    int64  `json:"fallbacks"`
	LastLLMError string `json:"last_llm_error,omitempty"`
}

func New(provider llm.Provider, reg *metrics.Registry, cfg Config) *Policy {
	if cfg.UCB <= 0 {
		cfg.UCB = 0.9
	}
	if cfg.Prior == 0 {
		cfg.Prior = 0.4
	}
	if cfg.LLMEvery <= 0 {
		cfg.LLMEvery = 12
	}
	if cfg.MaxRisk <= 0 {
		cfg.MaxRisk = 0.9
	}
	if cfg.MaxCands <= 0 {
		cfg.MaxCands = 24
	}
	if cfg.Seed == 0 {
		cfg.Seed = 1
	}
	if cfg.RepeatLimit <= 0 {
		cfg.RepeatLimit = 3
	}
	return &Policy{
		cfg: cfg, llm: provider, reg: reg, val: NewValidator(cfg.MaxRisk),
		q: map[string]map[string]*arm{}, rng: rand.New(rand.NewSource(cfg.Seed)),
	}
}

// StateKey is coarse on purpose: a bandit needs repeated visits to learn, and
// pixel-perfect states would never repeat. It still has to distinguish a menu
// from gameplay in an unknown game, so a few coarse visual buckets join the
// semantic fields. Without them every unknown screen collapses into one arm
// table and UCB happily loops on the first key it tried.
func StateKey(s domain.State) string {
	phase := strings.ToLower(strings.TrimSpace(s.Text["phase"]))
	if phase == "" {
		for _, t := range s.Tags {
			switch strings.ToLower(t) {
			case "menu", "title", "playing", "gameplay", "battle", "map", "dialog", "gameover":
				phase = strings.ToLower(t)
			}
			if phase != "" {
				break
			}
		}
	}
	if phase == "" {
		phase = "unknown"
	}
	hpBucket := 0
	if max := s.Values["max_hp"]; max > 0 {
		frac := s.Values["hp"] / max
		switch {
		case frac < 0.25:
			hpBucket = 1
		case frac < 0.5:
			hpBucket = 2
		case frac < 0.75:
			hpBucket = 3
		default:
			hpBucket = 4
		}
	}
	rooms := 0
	switch r := s.Values["room"]; {
	case r >= 9:
		rooms = 3
	case r >= 5:
		rooms = 2
	case r >= 3:
		rooms = 1
	}
	bright := 0
	switch b := s.Values["brightness"]; {
	case b >= 190:
		bright = 2
	case b >= 110:
		bright = 1
	}
	edge := 0
	switch e := s.Values["edge_ratio"]; {
	case e >= 0.10:
		edge = 2
	case e >= 0.03:
		edge = 1
	}
	dark, warm := 0, 0
	if s.Values["dark_ratio"] >= 0.5 {
		dark = 1
	}
	if s.Values["warm_ratio"] >= 0.4 {
		warm = 1
	}
	return fmt.Sprintf("%s|hp%d|room%d|v%d%d%d%d", phase, hpBucket, rooms, bright, edge, dark, warm)
}

func (p *Policy) Decide(ctx context.Context, in Input) Decision {
	start := time.Now()
	key := StateKey(in.State)
	legal, blocked := p.filter(in)
	cands := p.score(key, in.State, legal, in.Control)
	d := Decision{StateKey: key, Candidates: cands, Blocked: blocked}
	modal := isMenuScreen(in.State)

	useLLM := p.llm != nil && (in.ForceLLM || p.shouldLLM() || len(cands) == 0)
	if useLLM {
		if a, resp, err := p.askLLM(ctx, in, legal, cands); err == nil {
			d.Action = a
			d.Source = "llm"
			d.Provider = resp.Provider
			d.Model = resp.Model
			d.LLMUsed = true
			d.Score = 1
			d.Reason = "LLM policy choice"
			p.stats.LLMCalls++
		} else {
			p.stats.LLMFails++
			p.stats.LastLLMError = short(err.Error(), 240)
			d.Fallback = true
			d.Reason = "LLM unavailable: " + err.Error()
			if p.reg != nil {
				p.reg.Inc(metrics.PolicyFallbackTotal, 1)
			}
		}
	}

	if d.Action.Kind == "" {
		if len(cands) > 0 {
			best := cands[0]
			for _, c := range cands[1:] {
				if c.Score > best.Score {
					best = c
				}
			}
			d.Action = best.Action
			d.Score = best.Score
			d.Source = "heuristic"
			if d.Fallback {
				d.Reason += "; heuristic fallback"
			} else {
				d.Reason = "UCB + soft preference weights"
			}
		} else {
			d.Action = fallbackWait()
			d.Source = "fallback"
			d.Fallback = true
			d.Reason = "no legal action after constraints"
			p.stats.Fallbacks++
		}
	}

	// A direct user instruction is the strongest soft signal: the user is
	// watching the screen and knows what to press next. It only wins when the
	// action is legal right now, and it still goes through the validator below.
	if len(in.Directive) > 0 {
		for _, da := range in.Directive {
			if !isLegal(legal, da) {
				continue
			}
			d.Action = da
			d.Score = 1.2
			d.Source = "directive"
			d.Reason = in.DirectiveReason
			if d.Reason == "" {
				d.Reason = "执行用户指令：" + da.Reason
			}
			d.Fallback = false
			break
		}
	}

	// A learned demonstration is a strong hint, but never a hard override: it
	// only wins when the action is legal right now. The user's constraints have
	// already filtered the candidate set.
	if d.Source != "directive" && len(in.Demo) > 0 {
		for _, da := range in.Demo {
			if !isLegal(legal, da) {
				continue
			}
			d.Action = da
			d.Score = 1.0
			d.Source = "demo"
			d.Demo = true
			d.Reason = in.DemoReason
			if d.Reason == "" {
				d.Reason = "模仿人工示范"
			}
			d.Fallback = false
			break
		}
	}

	p.mu.Lock()
	idle := p.idleStreak
	p.mu.Unlock()
	if idle >= 2 && isPassive(d.Action) && !modal && d.Source != "directive" && d.Source != "demo" {
		if alt, ok := bestActive(cands); ok {
			d.Action = alt.Action
			d.Score = alt.Score
			d.Source = "explore"
			d.Reason = fmt.Sprintf("连续 %d 次等待/返回后强制探索：%s", idle, alt.Action.Key())
		}
	}

	// Break a verified loop: the same key pressed over and over is not
	// "learning", it is a stuck policy. Demonstrations are user intent and are
	// exempt; everything else alternates to the next legal active action.
	p.mu.Lock()
	repeat := p.lastActionStreak
	lastKey := p.lastActionKey
	lastReward := p.lastReward
	p.mu.Unlock()
	if repeat >= p.cfg.RepeatLimit && lastReward <= 0.05 && d.Source != "demo" && d.Source != "directive" && lastKey == d.Action.Key() {
		for _, c := range cands {
			if c.Action.Key() != d.Action.Key() && !isPassive(c.Action) {
				d.Action = c.Action
				d.Score = c.Score
				d.Source = "explore"
				d.Reason = fmt.Sprintf("连续 %d 次只重复 %s，换一个动作", repeat, lastKey)
				break
			}
		}
	}

	if err := p.val.Validate(d.Action, legal); err != nil && d.Action.Kind != "wait" {
		d.Fallback = true
		d.Source = "fallback"
		d.Reason = "validator rejected: " + err.Error()
		d.Action = bestLegalOrWait(legal)
	}
	d.ActionKey = d.Action.Key()
	d.LatencyMS = float64(time.Since(start).Microseconds()) / 1000.0
	p.mu.Lock()
	if isPassive(d.Action) {
		p.idleStreak++
	} else {
		p.idleStreak = 0
	}
	if d.ActionKey == p.lastActionKey {
		p.lastActionStreak++
	} else {
		p.lastActionKey = d.ActionKey
		p.lastActionStreak = 1
	}
	p.stats.Decisions++
	p.mu.Unlock()
	if p.reg != nil {
		p.reg.Observe(metrics.DecisionLatencyMS, d.LatencyMS)
		if d.Fallback {
			p.reg.Inc(metrics.PolicyFallbackTotal, 1)
		}
	}
	return d
}

// isPassive marks actions that do not interact with the game. "back" is treated
// as passive too: in an unfamiliar game it can leave the match instead of
// advancing it, so forced exploration prefers taps/swipes.
func isPassive(a domain.Action) bool {
	return a.Kind == "wait" || a.Kind == "back" || a.HasTag("idle")
}

func isLegal(legal []domain.Action, a domain.Action) bool {
	for _, l := range legal {
		if l.Key() == a.Key() {
			return true
		}
	}
	return false
}

// bestActive returns the highest-scored non-passive candidate from the already
// sorted list. It is only a nudge: if the profile offers no active action the
// decision stays unchanged.
func bestActive(cands []Candidate) (Candidate, bool) {
	for _, c := range cands {
		if !isPassive(c.Action) {
			return c, true
		}
	}
	return Candidate{}, false
}

// filter applies hard vetoes: instance constraints, forbidden tags, malformed
// actions, and financial-safety guards. Soft preferences never remove a
// candidate. The purchase guard is deliberately hard: a model must not be able
// to spend the user's money because it misread a menu label.
func (p *Policy) filter(in Input) ([]domain.Action, []string) {
	now := time.Now()
	forbidden := map[string]bool{}
	for _, t := range in.Rules.ForbiddenTags {
		forbidden[strings.ToLower(t)] = true
	}
	purchaseScreen := isPurchaseScreen(in.State)
	quitConfirm := isQuitConfirmScreen(in.State)
	adScreen := isAdScreen(in.State)
	var out []domain.Action
	var blocked []string
	for _, a := range in.Actions {
		if a.Kind == "" {
			continue
		}
		if denied, _ := in.Control.Denied(a, now); denied {
			continue
		}
		bad := false
		for _, t := range a.Tags {
			if forbidden[strings.ToLower(t)] {
				bad = true
				break
			}
		}
		if bad {
			continue
		}
		if isDestructiveAction(a) {
			blocked = append(blocked, "破坏性按钮: "+a.Key())
			continue
		}
		// A quit-confirmation dialog offers "play a bit longer" next to "exit
		// game". Back can also quit on some titles, so only the explicit stay
		// option (or waiting) is safe here.
		if quitConfirm && !isStayOnQuit(a) {
			blocked = append(blocked, "退出确认界面只允许继续: "+a.Key())
			continue
		}
		if isRiskyPurchaseAction(a) {
			blocked = append(blocked, "危险按钮: "+a.Key())
			continue
		}
		if isRiskyAdAction(a) {
			blocked = append(blocked, "广告/跳转按钮: "+a.Key())
			continue
		}
		if adScreen && (a.Kind == "tap" || a.Kind == "swipe") {
			blocked = append(blocked, "广告/跳转界面先返回: "+a.Key())
			continue
		}
		// On a payment/recharge screen only a safe escape is legal. Blind grid
		// taps and swipes could land on the purchase button, so they are blocked
		// even if OCR did not label them.
		if purchaseScreen && !isSafePurchaseEscape(a) {
			blocked = append(blocked, "支付界面仅允许关闭: "+a.Key())
			continue
		}
		out = append(out, a)
		if len(out) >= p.cfg.MaxCands {
			break
		}
	}
	if len(blocked) > 8 {
		blocked = blocked[:8]
	}
	return out, blocked
}

func (p *Policy) score(key string, st domain.State, legal []domain.Action, ctl domain.Control) []Candidate {
	c := p.cfg.UCB * (1 + 0.5*ctl.Weight("exploration"))
	if c <= 0 {
		c = p.cfg.UCB
	}
	p.mu.Lock()
	arms := p.q[key]
	total := p.total
	out := make([]Candidate, 0, len(legal))
	for _, a := range legal {
		ak := a.Key()
		var ar *arm
		if arms != nil {
			ar = arms[ak]
		}
		mean, visits := p.cfg.Prior, 0
		if ar != nil {
			mean, visits = ar.mean, ar.visits
		}
		bonus := c * math.Sqrt(math.Log(float64(total)+1)/float64(visits+1))
		adj := softAdjust(a, ctl) + survivalAdjust(a, st) + textTapAdjust(a, st) + navigationAdjust(a, st)
		// If the previous action produced a real scene transition, try the same
		// input again before moving on. This is game-agnostic: on a menu it
		// confirms the next step, in gameplay it keeps firing. It is only a
		// bonus, and the anti-loop rule still breaks a low-reward repetition.
		if p.lastReward >= 0.5 && p.lastActionKey == ak {
			adj += 0.8
		}
		out = append(out, Candidate{
			Action: a,
			Score:  mean + bonus + adj,
			Mean:   mean,
			Visits: visits,
		})
	}
	p.mu.Unlock()
	// Stable sort by score, then action key, so the reported order is
	// reproducible when scores tie.
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j].Score > out[i].Score || (out[j].Score == out[i].Score && out[j].Action.Key() < out[i].Action.Key()) {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

// survivalAdjust is a small, game-agnostic safety prior: when perceived HP is
// low, healing outranks damage. It is a baseline, not a replacement for
// learned values, and it never removes a legal action.
// textTapAdjust is game-agnostic: when OCR found readable labels, tapping one
// of them is far more meaningful than a blind grid cell. It is only a bonus;
// the action still has to be legal and the executor still checks focus.
func textTapAdjust(a domain.Action, st domain.State) float64 {
	if st.Value("text_count") <= 0 {
		return 0
	}
	if a.Kind != "tap_text" && !a.HasTag("ocr") {
		return 0
	}
	// OCR turned the screen into readable labels. Prefer labels that usually
	// move a game forward (start/continue/confirm/attack) and avoid labels that
	// open legal/rating/shop pages. A neutral label is usually HUD text (quests,
	// HP, tips), not a button: tapping it just burns a decision, so it must rank
	// below an exploratory grid tap regardless of past scene changes.
	semantic := textLabelAdjust(a.Target)
	if semantic == 0 {
		return -3.0
	}
	// When both "continue" and "new game" are visible, continuing the existing
	// save is almost always what the player wants. Starting a new game can erase
	// progress, so make it a clearly worse choice without hiding it completely.
	if isNewGameLabel(a.Target) && resumeLabelPresent(st) {
		semantic -= 3.0
	}
	return 1.2 + semantic
}

// textLabelAdjust scores a single OCR label by its wording. It is deliberately
// language-level and game-agnostic: no button coordinates, no per-game tables.
func textLabelAdjust(target string) float64 {
	label := strings.TrimSpace(strings.TrimPrefix(target, "text:"))
	if label == "" {
		return 0
	}
	lower := strings.ToLower(label)
	// Negative labels are checked first so "退出游戏" does not match "游戏".
	negative := []string{
		"适龄", "cadpa", "隐私", "协议", "儿童", "未成年", "实名", "防沉迷",
		"商城", "商店", "充值", "购买", "礼包", "广告", "评分", "分享",
		"退出", "注销", "删除账号", "shop", "store", "purchase", "buy now",
		"健康游戏忠告", "抵制不良游戏", "沉迷", "忠告",
	}
	for _, n := range negative {
		if strings.Contains(lower, n) {
			return -2.0
		}
	}
	positive := []string{
		"开始游戏", "继续游戏", "新的游戏", "新游戏", "进入游戏", "重新开始", "再玩一会",
		"攻击", "射击", "开火", "跳跃", "拾取", "采集", "制作", "使用", "装备",
		"对话", "返回", "下一", "挑战", "确认", "确定",
		"开始", "继续", "start game", "new game", "continue", "start", "play",
	}
	for _, p := range positive {
		if strings.Contains(lower, p) {
			return 2.4
		}
	}
	confirm := []string{"确定", "确认", "好的", "知道了", "同意", "进入", "ok", "yes", "confirm", "accept"}
	for _, p := range confirm {
		if strings.Contains(lower, p) {
			return 0.8
		}
	}
	if strings.Contains(lower, "关闭") || label == "×" || label == "x" {
		return 0.6
	}
	if len([]rune(label)) > 20 {
		return -0.6
	}
	return 0
}

// purchaseWords are strong signals that a payment or checkout UI is open. A
// generic "shop" button on the HUD is not enough: many games keep one visible at
// all times, and blocking every tap there would freeze normal play.
var purchaseWords = []string{
	"充值", "首充", "支付", "付款", "购买", "内购", "礼包", "确认支付", "立即购买",
	"支付宝", "微信支付", "花呗", "银行卡", "点券", "钻石", "余额不足",
	"purchase", "buy now", "checkout", "top up", "recharge", "payment",
}

// shopWords are risky to press on their own: they open a store, not a game
// action. They are blocked as actions but do not by themselves prove that a
// payment modal is currently open.
var shopWords = []string{"商城", "商店", "店铺", "商场", "shop", "store", "mall"}

// destructiveWords end the session instead of helping play. They are always
// blocked when they are an explicit button label; the system back key remains
// available so the agent can still escape a menu.
var stayWords = []string{"再玩一会", "继续游戏", "继续冒险", "继续旅程", "继续", "取消", "暂不", "稍后", "not now", "later", "continue"}

var destructiveWords = []string{
	"退出游戏", "退出登录", "结束游戏", "关闭游戏", "退出账号",
	"quit game", "exit game", "exit to desktop", "quit to desktop",
}

// adWords mark an ad or an out-of-game redirect. Tapping one can leave the
// game entirely (a real device test landed in the app store community page),
// so they are never valid game actions.
var adWords = []string{
	"点击查看", "查看详情", "了解更多", "立即查看", "去看看", "打开应用", "浏览器打开",
	"下载安装", "立即下载", "免费下载", "广告", "赞助", "推广", "安装",
}

var closeWords = []string{
	"关闭", "返回", "取消", "暂不", "以后再说", "稍后", "close", "cancel", "back", "not now", "later",
}

var menuWords = []string{
	"制作台", "工作台", "背包", "任务", "地图", "设置", "合成", "升级", "装备", "技能",
	"inventory", "crafting", "menu", "settings", "quest", "map", "upgrade",
}

func containsAny(s string, words []string) bool {
	for _, w := range words {
		if strings.Contains(s, w) {
			return true
		}
	}
	return false
}

func screenText(st domain.State) string {
	var b strings.Builder
	for _, v := range st.Text {
		b.WriteString(strings.ToLower(v))
		b.WriteString(" ")
	}
	b.WriteString(strings.ToLower(st.Summary))
	return b.String()
}

func isPurchaseScreen(st domain.State) bool {
	return containsAny(screenText(st), purchaseWords)
}

func isCloseLabel(s string) bool {
	label := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(s, "text:")))
	if label == "x" || label == "×" || label == "✕" || label == "✖" {
		return true
	}
	return containsAny(label, closeWords)
}

func isMenuScreen(st domain.State) bool {
	text := screenText(st)
	if containsAny(text, purchaseWords) {
		return true
	}
	// A menu word alone can be a permanent HUD button. It becomes a modal only
	// when the screen also exposes a close/back/cancel affordance.
	return containsAny(text, closeWords) && containsAny(text, menuWords)
}

func isQuitConfirmScreen(st domain.State) bool {
	text := screenText(st)
	return containsAny(text, destructiveWords) && containsAny(text, stayWords)
}

func isStayOnQuit(a domain.Action) bool {
	if a.Kind == "wait" {
		return true
	}
	if a.Kind != "tap_text" && a.Kind != "tap" {
		return false
	}
	label := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(a.Target, "text:")))
	return containsAny(label, stayWords) && !containsAny(label, destructiveWords)
}

func isDestructiveAction(a domain.Action) bool {
	if a.Kind == "back" || a.Kind == "wait" {
		return false
	}
	label := strings.ToLower(a.Target + " " + strings.Join(a.Tags, " "))
	return containsAny(label, destructiveWords)
}

func isAdScreen(st domain.State) bool {
	return containsAny(screenText(st), adWords)
}

func isRiskyAdAction(a domain.Action) bool {
	if a.Kind == "back" || a.Kind == "wait" {
		return false
	}
	label := strings.ToLower(a.Target + " " + strings.Join(a.Tags, " "))
	return containsAny(label, adWords)
}

func isRiskyPurchaseAction(a domain.Action) bool {
	if a.Kind == "back" || a.Kind == "wait" {
		return false
	}
	label := strings.ToLower(a.Target + " " + strings.Join(a.Tags, " "))
	return containsAny(label, purchaseWords) || containsAny(label, shopWords)
}

func isSafePurchaseEscape(a domain.Action) bool {
	if a.Kind == "back" || a.Kind == "wait" {
		return true
	}
	return isCloseLabel(a.Target)
}

func isNewGameLabel(s string) bool {
	label := strings.ToLower(strings.TrimPrefix(s, "text:"))
	return containsAny(label, []string{"新的游戏", "新游戏", "重新开始", "新档", "new game", "start new"})
}

func resumeLabelPresent(st domain.State) bool {
	return containsAny(screenText(st), []string{"继续游戏", "继续冒险", "继续旅程", "continue", "resume"})
}

// navigationAdjust makes modal escape a first-class choice instead of hoping a
// blind swipe eventually hits the close button. It is wording-based, so it
// works for any game that renders readable UI text.
func navigationAdjust(a domain.Action, st domain.State) float64 {
	if isPurchaseScreen(st) {
		switch {
		case isCloseLabel(a.Target):
			return 5.0
		case a.Kind == "back":
			return 4.0
		case a.Kind == "wait":
			return 0.5
		}
		return -4.0
	}
	if isQuitConfirmScreen(st) {
		// The safe exit is a real button ("再玩一会"/"继续游戏"), not a blind
		// wait. Waiting here keeps the dialog open forever, so the stay action
		// must clearly outrank idle.
		if a.Kind != "wait" && isStayOnQuit(a) {
			return 8.0
		}
		return 0
	}
	if isAdScreen(st) {
		switch {
		case a.Kind == "back":
			return 6.0
		case isCloseLabel(a.Target):
			return 5.0
		case a.Kind == "wait":
			return 0.3
		case a.Kind == "tap_text":
			return 0.5
		}
		return -4.0
	}
	if isMenuScreen(st) {
		switch {
		case isCloseLabel(a.Target):
			return 4.0
		case a.Kind == "back":
			return 2.5
		case a.Kind == "swipe":
			return -1.5
		case a.Kind == "tap":
			return -0.8
		case a.Kind == "tap_text":
			return -0.3
		}
	}
	return 0
}

// topCandidateActions keeps the highest-scored distinct actions for the
// on-device model. The heuristic still explores the full legal set; the model is
// only asked to choose among the actions that already look plausible.
func topCandidateActions(cands []Candidate, n int) []domain.Action {
	if n <= 0 {
		return nil
	}
	out := make([]domain.Action, 0, n)
	seen := map[string]bool{}
	for _, c := range cands {
		if seen[c.Action.Key()] {
			continue
		}
		seen[c.Action.Key()] = true
		out = append(out, c.Action)
		if len(out) >= n {
			break
		}
	}
	return out
}

// orderLocalActions puts close/back first for a small model choosing by index.
// A 0.5B model cannot reliably infer "this is a modal, escape it", so the
// ordering carries that prior while the hard filter still owns safety.
func orderLocalActions(legal []domain.Action, st domain.State) []domain.Action {
	if !isMenuScreen(st) {
		return legal
	}
	out := make([]domain.Action, 0, len(legal))
	for _, a := range legal {
		if a.Kind == "back" || isCloseLabel(a.Target) {
			out = append(out, a)
		}
	}
	for _, a := range legal {
		if a.Kind != "back" && !isCloseLabel(a.Target) {
			out = append(out, a)
		}
	}
	return out
}

func survivalAdjust(a domain.Action, st domain.State) float64 {
	maxHP := st.Value("max_hp")
	if maxHP <= 0 {
		return 0
	}
	frac := st.Value("hp") / maxHP
	if frac >= 0.4 {
		return 0
	}
	if a.Kind == "heal" || a.HasTag("heal") {
		return 4.0
	}
	if a.HasTag("guard") && frac < 0.2 {
		return 1.0
	}
	return 0
}

func softAdjust(a domain.Action, ctl domain.Control) float64 {
	adj := 0.0
	aggr := ctl.Weight("aggression")
	if a.HasTag("combat") || a.HasTag("damage") {
		adj += 0.8 * aggr
	}
	if a.HasTag("guard") {
		adj -= 0.4 * aggr
	}
	adj -= 2.0 * ctl.Weight("risk_aversion") * a.Risk
	if a.HasTag("advance") || a.HasTag("progress") {
		adj += 0.6 * ctl.Weight("speed")
	}
	if a.HasTag("heal") || a.Kind == "heal" {
		adj -= 0.5 * ctl.Weight("resource_aversion")
	}
	adj += ctl.Weight(a.Kind)
	if adj > 1.5 {
		adj = 1.5
	}
	if adj < -1.5 {
		adj = -1.5
	}
	return adj
}

func (p *Policy) Update(d Decision, reward float64) {
	p.UpdateKey(d.StateKey, d.ActionKey, reward)
}

func (p *Policy) UpdateKey(stateKey, actionKey string, reward float64) {
	if stateKey == "" || actionKey == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	arms := p.q[stateKey]
	if arms == nil {
		arms = map[string]*arm{}
		p.q[stateKey] = arms
	}
	ar := arms[actionKey]
	if ar == nil {
		ar = &arm{}
		arms[actionKey] = ar
	}
	ar.visits++
	ar.mean += (reward - ar.mean) / float64(ar.visits)
	p.total++
	p.lastReward = reward
	if reward > 0.3 {
		// A repeat that keeps paying off is intentional (firing, holding a
		// direction), not a stuck loop.
		p.lastActionStreak = 0
	}
	label := "progress"
	if reward <= 0.01 {
		label = "no visible progress"
	}
	p.recent = append(p.recent, fmt.Sprintf("%s => %s (reward %.2f)", actionKey, label, reward))
	if len(p.recent) > 5 {
		p.recent = p.recent[len(p.recent)-5:]
	}
}

func (p *Policy) shouldLLM() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.total%p.cfg.LLMEvery == 0
}

func (p *Policy) Stats() PolicyStats {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stats
}

func (p *Policy) QSize() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, arms := range p.q {
		n += len(arms)
	}
	return n
}

func (p *Policy) askLLM(ctx context.Context, in Input, legal []domain.Action, cands []Candidate) (domain.Action, llm.Response, error) {
	p.mu.Lock()
	recent := append([]string(nil), p.recent...)
	p.mu.Unlock()
	local := false
	if lp, ok := p.llm.(interface{ Local() bool }); ok {
		local = lp.Local()
	}
	prompt := buildPrompt(in, legal, recent)
	system := "You are the policy head of an external game-playing agent. You see only a game state summary. Choose exactly one of the listed legal actions. Never invent actions. Respect hard constraints absolutely. Preferences are soft hints. Reply with JSON only."
	timeout := 20 * time.Second
	maxTokens := 160
	localLegal := legal
	if local {
		// A 0.5B model on a phone processes a long prompt at roughly 50 tokens/s.
		// The API-sized prompt alone costs 15s and then times out, so give the
		// on-device model only the fields it actually needs. Picking an index is
		// also more reliable than asking a tiny model to reproduce kind/target:
		// it used to echo OCR text such as "外部感知,本机小模型" as an action.
		// Drop the passive wait action: a tiny model kept selecting it, which
		// looked like the AI had frozen. wait stays available through directives
		// and through the heuristic path.
		// A phone-sized model does not need every legal tap: the top scored
		// candidates already encode the meaningful OCR labels, the modal escape,
		// and a little exploration. Sending all 27 actions doubled prompt
		// processing time and pushed one choice to ~19s on the test device.
		base := legal
		if top := topCandidateActions(cands, 8); len(top) > 0 {
			base = top
		}
		withoutWait := make([]domain.Action, 0, len(base))
		for _, a := range base {
			if a.Kind != "wait" {
				withoutWait = append(withoutWait, a)
			}
		}
		if len(withoutWait) > 0 {
			localLegal = orderLocalActions(withoutWait, in.State)
		}
		prompt = buildLocalPrompt(in, localLegal, recent)
		system = "You are the action selector of a game-playing agent. Pick exactly one numbered action from the list. Reply only JSON: {\"index\": N}. Never copy screen text. Never invent an action."
		timeout = 30 * time.Second
		maxTokens = 24
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	resp, err := p.llm.Complete(ctx, llm.Request{
		System:      system,
		Prompt:      prompt,
		Temperature: 0.2,
		MaxTokens:   maxTokens,
		JSON:        true,
	})
	if err != nil {
		return domain.Action{}, resp, err
	}
	js, ok := llm.ExtractJSON(resp.Text)
	if !ok {
		return domain.Action{}, resp, fmt.Errorf("no JSON in response: %q", short(resp.Text, 120))
	}
	var pick struct {
		Index      int     `json:"index"`
		Kind       string  `json:"kind"`
		Action     string  `json:"action"`
		Target     string  `json:"target"`
		Reason     string  `json:"reason"`
		Confidence float64 `json:"confidence"`
	}
	if err := json.Unmarshal([]byte(js), &pick); err != nil {
		return domain.Action{}, resp, fmt.Errorf("bad JSON: %w", err)
	}
	if local {
		if pick.Index < 1 || pick.Index > len(localLegal) {
			return domain.Action{}, resp, fmt.Errorf("LLM picked illegal action index=%d (1..%d)", pick.Index, len(localLegal))
		}
		return localLegal[pick.Index-1], resp, nil
	}
	kind := pick.Kind
	if kind == "" {
		kind = pick.Action
	}
	if a, ok := groundLLMAction(kind, pick.Target, legal); ok {
		return a, resp, nil
	}
	return domain.Action{}, resp, fmt.Errorf("LLM picked illegal action kind=%q target=%q", kind, pick.Target)
}

// groundLLMAction maps a small model's choice onto the exact legal action set.
// Small models often echo an annotated target ("enemy 10/28") or omit the
// target. The rule layer owns legality, so we accept a kind+target match and
// fall back to the unique legal action of that kind instead of rejecting a
// usable choice. This never invents an action: the result always comes from
// legal.
func groundLLMAction(kind, target string, legal []domain.Action) (domain.Action, bool) {
	kind = strings.TrimSpace(kind)
	target = strings.TrimSpace(target)
	if kind == "" {
		return domain.Action{}, false
	}
	var sameKind []domain.Action
	for _, a := range legal {
		if !strings.EqualFold(a.Kind, kind) {
			continue
		}
		if target != "" && strings.EqualFold(a.Target, target) {
			return a, true
		}
		sameKind = append(sameKind, a)
	}
	if len(sameKind) == 1 {
		return sameKind[0], true
	}
	if target != "" {
		lt := strings.ToLower(target)
		for _, a := range sameKind {
			at := strings.ToLower(a.Target)
			if at != "" && (strings.Contains(lt, at) || strings.Contains(at, lt)) {
				return a, true
			}
		}
	}
	return domain.Action{}, false
}

func buildPrompt(in Input, legal []domain.Action, recent []string) string {
	var b strings.Builder
	if in.GameName != "" || in.GamePackage != "" {
		b.WriteString("GAME: ")
		b.WriteString(in.GameName)
		if in.GamePackage != "" {
			b.WriteString(" (package ")
			b.WriteString(in.GamePackage)
			b.WriteString(")")
		}
		b.WriteString("\n")
	}
	if in.GamePrior != "" {
		b.WriteString("GAME PRIOR (soft hypothesis from a previous analysis; use it to form expectations, do not invent actions):\n")
		b.WriteString(short(in.GamePrior, 600))
		b.WriteString("\n")
	}
	b.WriteString("STATE: ")
	b.WriteString(in.State.Summary)
	b.WriteString("\n")
	if isPurchaseScreen(in.State) {
		b.WriteString("SCREEN: payment/recharge dialog. Only close/back is legal; never buy, recharge, or confirm payment.\n")
	} else if isMenuScreen(in.State) {
		b.WriteString("SCREEN: menu/modal. Prefer close/back over scrolling or blind taps.\n")
	}
	if len(in.State.Values) > 0 {
		b.WriteString("VALUES:")
		for _, k := range domain.SortedKeys(in.State.Values) {
			fmt.Fprintf(&b, " %s=%.2f", k, in.State.Values[k])
		}
		b.WriteString("\n")
	}
	if len(in.Rules.Notes) > 0 {
		b.WriteString("INSTANCE RULES: ")
		b.WriteString(strings.Join(in.Rules.Notes, "; "))
		b.WriteString("\n")
	}
	if len(recent) > 0 {
		b.WriteString("RECENT RESULTS (do not repeat a failed loop):\n")
		for _, line := range recent {
			b.WriteString("- " + line + "\n")
		}
	}
	if len(in.Control.Hard) > 0 {
		b.WriteString("HARD CONSTRAINTS (must obey):\n")
		for _, c := range in.Control.Hard {
			if c.IsActive(time.Now()) {
				fmt.Fprintf(&b, "- %s (deny actions=%v targets=%v tags=%v)\n", c.Text, c.DenyActions, c.DenyTargets, c.DenyTags)
			}
		}
	}
	if len(in.Hints) > 0 || len(in.Control.Hints) > 0 {
		b.WriteString("SOFT PREFERENCES (hints only):\n")
		for _, h := range append(append([]string{}, in.Control.Hints...), in.Hints...) {
			b.WriteString("- " + h + "\n")
		}
	}
	b.WriteString("LEGAL ACTIONS:\n")
	for i, a := range legal {
		fmt.Fprintf(&b, "%d. kind=%s target=%s risk=%.2f tags=%s\n", i+1, a.Kind, a.Target, a.Risk, strings.Join(a.Tags, ","))
	}
	b.WriteString("Reply JSON: {\"kind\":\"...\",\"target\":\"...\",\"reason\":\"...\",\"confidence\":0.0}")
	return b.String()
}

// buildLocalPrompt is the small-model variant: no prose sections, no long
// memory lines, and a compact action list. Every action is still named exactly
// as the validator expects, so the reply grounds against the same legal set.
func buildLocalPrompt(in Input, legal []domain.Action, recent []string) string {
	var b strings.Builder
	if in.GameName != "" {
		b.WriteString("GAME: " + short(in.GameName, 60) + "\n")
	}
	b.WriteString("STATE: " + short(in.State.Summary, 200) + "\n")
	if isPurchaseScreen(in.State) {
		b.WriteString("SCREEN: payment. Only close/back.\n")
	} else if isMenuScreen(in.State) {
		b.WriteString("SCREEN: menu. Close/back first.\n")
	}
	if len(in.State.Values) > 0 {
		b.WriteString("VALUES:")
		n := 0
		for _, k := range domain.SortedKeys(in.State.Values) {
			if n >= 6 {
				break
			}
			fmt.Fprintf(&b, " %s=%.1f", k, in.State.Values[k])
			n++
		}
		b.WriteString("\n")
	}
	if len(recent) > 0 {
		b.WriteString("RECENT:")
		start := len(recent) - 3
		if start < 0 {
			start = 0
		}
		for _, line := range recent[start:] {
			b.WriteString(" " + short(line, 70) + ";")
		}
		b.WriteString("\n")
	}
	if len(in.Control.Hard) > 0 {
		b.WriteString("HARD:")
		for _, c := range in.Control.Hard {
			if c.IsActive(time.Now()) {
				b.WriteString(" " + short(c.Text, 80) + ";")
			}
		}
		b.WriteString("\n")
	}
	if hints := append(append([]string{}, in.Control.Hints...), in.Hints...); len(hints) > 0 {
		b.WriteString("PREF:")
		for i, h := range hints {
			if i >= 3 {
				break
			}
			b.WriteString(" " + short(h, 70) + ";")
		}
		b.WriteString("\n")
	}
	b.WriteString("ACTIONS:")
	for i, a := range legal {
		fmt.Fprintf(&b, "\n%d. %s·%s", i+1, a.Kind, a.Target)
	}
	b.WriteString("\nReply JSON: {\"index\":1}")
	return b.String()
}

func fallbackWait() domain.Action {
	return domain.Action{
		Kind: "wait", Target: "self", Tags: []string{"wait", "safe"},
		Reason: "safe fallback", Risk: 0,
		Input: &domain.InputSpec{Type: "wait", DurationMS: 200},
	}
}

func bestLegalOrWait(legal []domain.Action) domain.Action {
	if len(legal) > 0 {
		return legal[0]
	}
	return fallbackWait()
}

func short(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
