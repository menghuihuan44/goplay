package policy

import (
	"context"
	"strings"
	"testing"

	"goplay/internal/domain"
	"goplay/internal/llm"
)

func mockState() domain.State {
	return domain.State{
		Summary: "room 1 hp 100 enemy 20",
		Values:  map[string]float64{"hp": 100, "max_hp": 100, "room": 1, "enemy_hp": 20},
		Text:    map[string]string{"phase": "combat"},
		Tags:    []string{"room1", "combat"},
	}
}

func legalActions() []domain.Action {
	return []domain.Action{
		{Kind: "attack", Target: "enemy", Tags: []string{"combat", "damage"}, Risk: 0.35, Input: &domain.InputSpec{Type: "key", Key: "a"}},
		{Kind: "defend", Target: "enemy", Tags: []string{"combat", "guard"}, Risk: 0.15, Input: &domain.InputSpec{Type: "key", Key: "d"}},
		{Kind: "advance", Target: "next_room", Tags: []string{"advance", "progress"}, Risk: 0.2, Input: &domain.InputSpec{Type: "key", Key: "w"}},
	}
}

func touchState(text string) domain.State {
	return domain.State{
		Summary: "touch screen",
		Text:    map[string]string{"ui_text": text},
		Values:  map[string]float64{"text_count": 4, "brightness": 100},
		Tags:    []string{"frame", "ui_text"},
	}
}

func tapText(label string) domain.Action {
	return domain.Action{Kind: "tap_text", Target: "text:" + label, Tags: []string{"ui", "text", "ocr", label}, Risk: 0.1, Input: &domain.InputSpec{Type: "tap", X: 0.5, Y: 0.5}}
}

func TestPurchaseScreenOnlyAllowsEscape(t *testing.T) {
	p := New(nil, nil, DefaultConfig())
	actions := []domain.Action{
		tapText("充值 60"),
		tapText("关闭"),
		{Kind: "back", Target: "screen", Tags: []string{"ui", "back"}, Input: &domain.InputSpec{Type: "back"}},
		{Kind: "swipe", Target: "screen_up", Tags: []string{"ui", "scroll"}, Input: &domain.InputSpec{Type: "swipe", Y: 0.7, Y2: 0.3}},
		{Kind: "tap", Target: "screen_2_2", Tags: []string{"ui", "select"}, Input: &domain.InputSpec{Type: "tap", X: 0.5, Y: 0.5}},
	}
	d := p.Decide(context.Background(), Input{State: touchState("商城 充值 首充 礼包 关闭"), Actions: actions})
	if d.Action.Target == "text:充值 60" || d.Action.Kind == "swipe" || d.Action.Kind == "tap" {
		t.Fatalf("payment screen chose a dangerous action: %+v", d.Action)
	}
	if d.Action.Kind != "tap_text" || !strings.Contains(d.Action.Target, "关闭") {
		t.Fatalf("payment screen should close, got %+v", d.Action)
	}
	if len(d.Blocked) == 0 {
		t.Fatalf("dangerous actions should be reported as blocked")
	}
}

func TestDestructiveExitIsBlocked(t *testing.T) {
	p := New(nil, nil, DefaultConfig())
	actions := []domain.Action{
		tapText("再玩一会"),
		tapText("退出游戏"),
		{Kind: "back", Target: "screen", Tags: []string{"ui", "back"}, Input: &domain.InputSpec{Type: "back"}},
	}
	d := p.Decide(context.Background(), Input{State: touchState("健康游戏忠告 再玩一会 退出游戏"), Actions: actions})
	if strings.Contains(d.Action.Target, "退出游戏") {
		t.Fatalf("chose to quit the game: %+v", d.Action)
	}
	if d.Action.Target != "text:再玩一会" {
		t.Fatalf("should keep playing, got %+v", d.Action)
	}
	if len(d.Blocked) == 0 {
		t.Fatalf("quit button should be reported as blocked")
	}
}

func TestAdRedirectIsBlocked(t *testing.T) {
	p := New(nil, nil, DefaultConfig())
	actions := []domain.Action{
		{Kind: "back", Target: "screen", Tags: []string{"ui", "back"}, Input: &domain.InputSpec{Type: "back"}},
		{Kind: "tap", Target: "screen_2_2", Tags: []string{"ui"}, Input: &domain.InputSpec{Type: "tap", X: 0.5, Y: 0.55}},
		tapText("点击查看>"),
		tapText("了解更多"),
	}
	d := p.Decide(context.Background(), Input{State: touchState("广告 点击查看> 了解更多 下载安装"), Actions: actions})
	if d.Action.Kind != "back" {
		t.Fatalf("an ad/redirect screen should escape with back, got %+v", d.Action)
	}
	if len(d.Blocked) == 0 {
		t.Fatalf("ad buttons and blind taps should be reported as blocked")
	}
}

func TestContinueBeatsNewGame(t *testing.T) {
	p := New(nil, nil, DefaultConfig())
	actions := []domain.Action{tapText("继续游戏"), tapText("新的游戏")}
	d := p.Decide(context.Background(), Input{State: touchState("继续游戏 新的游戏"), Actions: actions})
	if d.Action.Target != "text:继续游戏" {
		t.Fatalf("existing save should win over new game, got %+v", d.Action)
	}
}

func TestMenuPrefersCloseOverScrolling(t *testing.T) {
	p := New(nil, nil, DefaultConfig())
	actions := []domain.Action{
		{Kind: "swipe", Target: "screen_up", Tags: []string{"ui", "scroll"}, Input: &domain.InputSpec{Type: "swipe", Y: 0.7, Y2: 0.3}},
		{Kind: "back", Target: "screen", Tags: []string{"ui", "back"}, Input: &domain.InputSpec{Type: "back"}},
		tapText("关闭"),
		tapText("制作"),
	}
	d := p.Decide(context.Background(), Input{State: touchState("制作台 背包 关闭 返回"), Actions: actions})
	if d.Action.Kind == "swipe" {
		t.Fatalf("menu should not scroll forever, got %+v", d.Action)
	}
	if d.Action.Kind != "tap_text" && d.Action.Kind != "back" {
		t.Fatalf("menu should escape, got %+v", d.Action)
	}
}

func TestHudShopButtonDoesNotFreezePlay(t *testing.T) {
	p := New(nil, nil, DefaultConfig())
	actions := []domain.Action{tapText("继续游戏"), tapText("商城"), {Kind: "tap", Target: "screen_2_2", Tags: []string{"ui", "select"}, Input: &domain.InputSpec{Type: "tap", X: 0.5, Y: 0.5}}}
	d := p.Decide(context.Background(), Input{State: touchState("任务 地图 商城 继续游戏"), Actions: actions})
	if d.Action.Target != "text:继续游戏" {
		t.Fatalf("a HUD shop button must not block normal play, got %+v", d.Action)
	}
}

func TestLocalPromptStaysSmall(t *testing.T) {
	in := Input{State: mockState(), Actions: legalActions(), GameName: "A Very Long Game Name", GamePrior: strings.Repeat("prior ", 200)}
	prompt := buildLocalPrompt(in, legalActions(), []string{"one", "two", "three", "four"})
	if len(prompt) > 1200 {
		t.Fatalf("local prompt too large for a phone model: %d chars", len(prompt))
	}
	for _, want := range []string{"ACTIONS:", "attack·enemy", "Reply JSON"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("local prompt missing %q: %s", want, prompt)
		}
	}
	if strings.Contains(prompt, "RECENT: one") {
		t.Fatalf("local prompt should keep only the newest recent lines: %s", prompt)
	}
}

func TestConstraintDeniesHard(t *testing.T) {
	p := New(nil, nil, DefaultConfig())
	ctl := domain.Control{Hard: []domain.Constraint{{
		ID: "c1", Text: "don't attack", DenyActions: []string{"attack"}, Active: true,
	}}}
	d := p.Decide(context.Background(), Input{State: mockState(), Actions: legalActions(), Control: ctl})
	if d.Action.Kind == "attack" {
		t.Fatalf("constraint should remove attack, got %+v", d.Action)
	}
}

func TestSoftPreferenceNeverFilters(t *testing.T) {
	p := New(nil, nil, DefaultConfig())
	ctl := domain.Control{Weights: domain.SoftWeights{"aggression": -1, "risk_aversion": 1}}
	d := p.Decide(context.Background(), Input{State: mockState(), Actions: legalActions(), Control: ctl})
	if len(d.Candidates) != 3 {
		t.Fatalf("soft prefs must not filter candidates, got %d", len(d.Candidates))
	}
	if d.Action.Kind == "attack" {
		t.Fatalf("aggression -1 + risk_aversion 1 should prefer a safer action, got %+v", d.Action)
	}
}

func TestUCBLearns(t *testing.T) {
	p := New(nil, nil, DefaultConfig())
	st := mockState()
	rewards := map[string]float64{"attack": 1.0, "defend": 0.0, "advance": 0.1}
	counts := map[string]int{}
	for i := 0; i < 300; i++ {
		d := p.Decide(context.Background(), Input{State: st, Actions: legalActions()})
		counts[d.Action.Kind]++
		p.Update(d, rewards[d.Action.Kind])
	}
	if counts["attack"] < 250 {
		t.Fatalf("UCB failed to exploit: %+v", counts)
	}
}

type fixedLLM struct{ reply string }

func (f fixedLLM) Name() string                   { return "fixed" }
func (f fixedLLM) Available(context.Context) bool { return true }
func (f fixedLLM) Complete(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{Text: f.reply, Provider: "fixed", Model: "mock"}, nil
}

type localLLM struct{ fixedLLM }

func (localLLM) Local() bool { return true }

func TestLocalLLMPicksByIndex(t *testing.T) {
	p := New(localLLM{fixedLLM{reply: `{"index":2}`}}, nil, DefaultConfig())
	d := p.Decide(context.Background(), Input{State: mockState(), Actions: legalActions(), ForceLLM: true})
	// Index is 1-based into the same compact candidate list the prompt showed.
	// With the fixed mock state the stable order starts advance, attack, defend.
	if d.Action.Kind != "attack" || d.Source != "llm" {
		t.Fatalf("expected index 2 to select the second listed action (attack), got %+v", d)
	}
}

func TestLocalLLMBadIndexFallsBack(t *testing.T) {
	p := New(localLLM{fixedLLM{reply: `{"index":99}`}}, nil, DefaultConfig())
	d := p.Decide(context.Background(), Input{State: mockState(), Actions: legalActions(), ForceLLM: true})
	if !d.Fallback {
		t.Fatalf("out-of-range index must fall back, got %+v", d)
	}
}

func TestLLMChoiceValidated(t *testing.T) {
	p := New(fixedLLM{reply: `{"kind":"defend","target":"enemy","confidence":0.9}`}, nil, DefaultConfig())
	d := p.Decide(context.Background(), Input{State: mockState(), Actions: legalActions(), ForceLLM: true})
	if d.Action.Kind != "defend" || d.Source != "llm" {
		t.Fatalf("expected llm defend, got %+v", d)
	}
}

func TestLLMAnnotatedTargetIsGrounded(t *testing.T) {
	// Small local models often repeat an annotated target. The rule layer must
	// map it to the exact legal action rather than rejecting a usable choice.
	p := New(fixedLLM{reply: `{"kind":"attack","target":"enemy 10/28"}`}, nil, DefaultConfig())
	d := p.Decide(context.Background(), Input{State: mockState(), Actions: legalActions(), ForceLLM: true})
	if d.Action.Kind != "attack" || d.Source != "llm" || d.Fallback {
		t.Fatalf("expected grounded llm attack, got %+v", d)
	}
	if d.Action.Target != "enemy" {
		t.Fatalf("target must be the legal canonical target, got %q", d.Action.Target)
	}
}

func TestLLMIllegalFallsBack(t *testing.T) {
	p := New(fixedLLM{reply: `{"kind":"nuke","target":"everything"}`}, nil, DefaultConfig())
	d := p.Decide(context.Background(), Input{State: mockState(), Actions: legalActions(), ForceLLM: true})
	if !d.Fallback || d.Action.Kind == "nuke" {
		t.Fatalf("illegal LLM action must fall back, got %+v", d)
	}
	if !strings.Contains(d.Reason, "illegal") && !strings.Contains(d.Reason, "fallback") {
		t.Fatalf("unexpected reason %q", d.Reason)
	}
}

func TestForcedExplorationAfterIdleStreak(t *testing.T) {
	// An unfamiliar touch game can look alive while the policy keeps choosing
	// wait/back. After two passive decisions it must try a real interaction.
	p := New(nil, nil, DefaultConfig())
	st := mockState()
	wait := domain.Action{Kind: "wait", Target: "screen", Input: &domain.InputSpec{Type: "wait", DurationMS: 300}}
	tap := domain.Action{Kind: "tap", Target: "center", Risk: 0.1, Input: &domain.InputSpec{Type: "tap", X: 0.5, Y: 0.5}}
	for i := 0; i < 100; i++ {
		p.UpdateKey(StateKey(st), tap.Key(), 0)
	}
	p.idleStreak = 2
	d := p.Decide(context.Background(), Input{State: st, Actions: []domain.Action{wait, tap}})
	if d.Source != "explore" || d.Action.Kind != "tap" {
		t.Fatalf("expected forced exploration tap, got %+v", d)
	}
	if !strings.Contains(d.Reason, "强制探索") {
		t.Fatalf("unexpected reason %q", d.Reason)
	}
}

func TestValidatorRejectsBadCoords(t *testing.T) {
	v := NewValidator(0.9)
	legal := []domain.Action{{Kind: "click", Target: "screen", Risk: 0.1, Input: &domain.InputSpec{Type: "click", X: 2, Y: 0.5}}}
	if err := v.Validate(legal[0], legal); err == nil {
		t.Fatal("expected coordinate rejection")
	}
}
func TestValidatorAllowsBack(t *testing.T) {
	v := NewValidator(0.9)
	legal := []domain.Action{{Kind: "back", Target: "screen", Risk: 0.05, Input: &domain.InputSpec{Type: "back"}}}
	if err := v.Validate(legal[0], legal); err != nil {
		t.Fatalf("back must be allowlisted: %v", err)
	}
}

func TestDemoHintWinsOnlyWhenLegal(t *testing.T) {
	p := New(nil, nil, DefaultConfig())
	legal := []domain.Action{
		{Kind: "press_enter", Target: "menu", Risk: 0.05, Input: &domain.InputSpec{Type: "key", Key: "enter"}},
		{Kind: "wait", Target: "screen", Risk: 0, Input: &domain.InputSpec{Type: "wait"}},
	}
	demo := domain.Action{Kind: "demo_key_z", Target: "game", Risk: 0.05,
		Input: &domain.InputSpec{Type: "key", Key: "z"}}
	d := p.Decide(context.Background(), Input{State: mockState(), Actions: legal, Demo: []domain.Action{demo}})
	if d.Action.Kind == "demo_key_z" {
		t.Fatalf("demo action outside the legal set must not be used: %+v", d)
	}
	legal = append(legal, demo)
	d = p.Decide(context.Background(), Input{State: mockState(), Actions: legal, Demo: []domain.Action{demo}, DemoReason: "模仿示范"})
	if d.Action.Kind != "demo_key_z" || d.Source != "demo" || !d.Demo {
		t.Fatalf("expected legal demo hint, got kind=%q source=%q demo=%v", d.Action.Kind, d.Source, d.Demo)
	}
}
func TestDirectiveWinsWhenLegal(t *testing.T) {
	p := New(nil, nil, DefaultConfig())
	directive := domain.Action{Kind: "press_enter", Target: "menu", Risk: 0.05, Input: &domain.InputSpec{Type: "key", Key: "enter"}}
	legal := append(legalActions(), directive)
	d := p.Decide(context.Background(), Input{State: mockState(), Actions: legal, Directive: []domain.Action{directive}, DirectiveReason: "执行用户指令：按回车"})
	if d.Action.Kind != "press_enter" || d.Source != "directive" {
		t.Fatalf("expected directive enter, got kind=%q source=%q", d.Action.Kind, d.Source)
	}
}

func TestRepeatBreaksStuckLoop(t *testing.T) {
	p := New(fixedLLM{reply: `{"kind":"attack","target":"enemy"}`}, nil, DefaultConfig())
	st := mockState()
	attack := legalActions()[0]
	p.lastActionKey = attack.Key()
	p.lastActionStreak = p.cfg.RepeatLimit
	p.lastReward = 0
	d := p.Decide(context.Background(), Input{State: st, Actions: legalActions(), ForceLLM: true})
	if d.Action.Kind == "attack" {
		t.Fatalf("repeated zero-reward attack should be broken, got %+v", d)
	}
	if d.Source != "explore" {
		t.Fatalf("expected explore source, got %q", d.Source)
	}
}
func TestOCRLabelPrefersStartOverRating(t *testing.T) {
	p := New(nil, nil, DefaultConfig())
	st := mockState()
	st.Values["text_count"] = 2
	start := domain.Action{Kind: "tap_text", Target: "text:开始游戏", Tags: []string{"ui", "text", "ocr"}, Risk: 0.1, Input: &domain.InputSpec{Type: "tap", X: 0.5, Y: 0.7}}
	rating := domain.Action{Kind: "tap_text", Target: "text:12+适龄提示", Tags: []string{"ui", "text", "ocr"}, Risk: 0.1, Input: &domain.InputSpec{Type: "tap", X: 0.1, Y: 0.85}}
	d := p.Decide(context.Background(), Input{State: st, Actions: []domain.Action{rating, start}})
	if d.Action.Target != "text:开始游戏" {
		t.Fatalf("expected start label, got %+v", d.Action)
	}
	if textLabelAdjust("text:开始游戏") <= textLabelAdjust("text:12+适龄提示") {
		t.Fatal("start label should outrank rating label")
	}
}

func TestHUDTextIsNotAButton(t *testing.T) {
	// Quest text, HP numbers and legal notices are labels the OCR can read,
	// but they are not buttons. They must not outrank a legal grid tap.
	for _, label := range []string{
		"text:主线前往码头", "text:215/215", "text:40/200 +",
		"text:健康游戏忠告", "text:当前追踪",
	} {
		if got := textLabelAdjust(label); got > 0 {
			t.Fatalf("%s should be neutral or negative, got score %v", label, got)
		}
	}
	for _, label := range []string{"text:继续游戏", "text:攻击", "text:确认"} {
		if got := textLabelAdjust(label); got <= 0 {
			t.Fatalf("%s should be actionable, got score %v", label, got)
		}
	}
}
