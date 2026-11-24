package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"goplay/internal/domain"
	"goplay/internal/localruntime"
)

// diagCheck is one verifiable fact shown in the Android diagnostics panel.
type diagCheck struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

// diagSuggestion is an actionable repair hint. Action is a stable token the
// client maps to a button.
type diagSuggestion struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Detail string `json:"detail"`
	Action string `json:"action"`
}

type diagBridge struct {
	Connected      bool   `json:"connected"`
	Client         string `json:"client"`
	Device         string `json:"device"`
	Frames         int64  `json:"frames"`
	Actions        int64  `json:"actions"`
	Errors         int64  `json:"errors"`
	LastFrameAgeMS int64  `json:"last_frame_age_ms"`
	Human          bool   `json:"human"`
}

type diagInstance struct {
	ID        string     `json:"id"`
	Status    string     `json:"status"`
	Profile   string     `json:"profile"`
	Source    string     `json:"source"`
	Actions   int64      `json:"actions"`
	Errors    int64      `json:"errors"`
	Decisions int64      `json:"decisions"`
	Wins      int64      `json:"wins"`
	Losses    int64      `json:"losses"`
	Progress  float64    `json:"progress"`
	FrameAt   *time.Time `json:"frame_at,omitempty"`
}

type diagResult struct {
	OK          bool             `json:"ok"`
	Status      string           `json:"status"`
	Code        string           `json:"code"`
	Title       string           `json:"title"`
	Detail      string           `json:"detail"`
	Phase       string           `json:"phase"`
	UserFixable bool             `json:"user_fixable"`
	Cause       string           `json:"cause"`
	LastError   string           `json:"last_error"`
	Checks      []diagCheck      `json:"checks"`
	Suggestions []diagSuggestion `json:"suggestions"`
	Bridge      diagBridge       `json:"bridge"`
	Instance    diagInstance     `json:"instance"`
}

// diagInput is the pure input to classifyDiagnostic. Keeping it separate from
// manager types makes the classification table testable without HTTP or timers.
type diagInput struct {
	Status          domain.InstanceStatus
	Source          string
	LastError       string
	Human           bool
	BridgeConnected bool
	Frames          int64
	LastFrameAgeMS  int64
	BridgeErrors    int64
	InstanceActions int64
	LLMMode         string
	LLMAvailable    bool
	ModelMissing    bool
}

func classifyDiagnostic(in diagInput) diagResult {
	res := diagResult{
		Status:    string(in.Status),
		Phase:     string(in.Status),
		LastError: in.LastError,
		Checks:    diagnosticChecks(in),
	}
	code := "ok"
	title := "实例运行正常"
	detail := "未发现需要处理的问题。"
	cause := "healthy"
	fixable := false

	errText := strings.ToLower(strings.TrimSpace(in.LastError))
	isAndroid := in.Source == "android"

	switch {
	case in.Human:
		code, title, detail, cause, fixable = "external_control", "人工接管中", "当前由人工控制，AI 已暂停决策。交还控制后可恢复自动运行。", "human_control", true
	case errText != "":
		switch {
		case strings.Contains(errText, "ack timeout"):
			code, title, detail, cause, fixable = "android_ack_timeout", "手机操作超时", "已发送操作，但手机端没有在时限内确认。请确认无障碍服务正常后重试。", "ack_timeout", true
		case strings.Contains(errText, "android action") || strings.Contains(errText, "send action"):
			code, title, detail, cause, fixable = "android_action_failed", "手机操作失败", "Android 端返回操作失败，通常是因为无障碍权限不足或目标控件已变化。", "action_failed", true
		case strings.Contains(errText, "capture:"):
			code, title, detail, cause, fixable = "capture_failed", "屏幕采集失败", "实例无法获取画面。请检查屏幕录制授权和采集源是否可用。", "capture", true
		case strings.Contains(errText, "no android client"):
			code, title, detail, cause, fixable = "android_no_client", "未连接 Android 客户端", "实例正在等待手机端连接。请确认手机 App 已打开并连接到当前实例。", "no_client", true
		case strings.Contains(errText, "model") && (strings.Contains(errText, "missing") || strings.Contains(errText, "not found") || strings.Contains(errText, "download") || strings.Contains(errText, "not running") || strings.Contains(errText, "no model") || strings.Contains(errText, "unavailable")):
			code, title, detail, cause, fixable = "model_missing", "本地模型未就绪", "本地推理模型缺失或尚未下载完成，AI 暂时无法决策。", "model_missing", true
		case strings.Contains(errText, "llm") || strings.Contains(errText, "provider") || strings.Contains(errText, "no provider"):
			code, title, detail, cause, fixable = "llm_unavailable", "AI 模型不可用", "当前没有可用的大模型提供方，AI 无法生成决策。", "llm_unavailable", true
		default:
			code, title, detail, cause = "unknown", "发生未知错误", "错误信息无法自动归类，请查看日志定位具体原因。", "internal"
		}
	case isAndroid && !in.BridgeConnected:
		code, title, detail, cause, fixable = "android_no_client", "未连接 Android 客户端", "实例正在等待手机端连接。请确认手机 App 已打开、实例已激活且 WebSocket 未断开。", "no_client", true
	case isAndroid && (in.Frames == 0 || (in.LastFrameAgeMS >= 0 && in.LastFrameAgeMS > 5000)):
		code, title, detail, cause, fixable = "android_no_client", "实时画面超时", "手机已连接，但超过 5 秒没有收到新画面。可能是屏幕录制权限被系统暂停。", "frame_timeout", true
	}

	if code == "ok" && in.LLMMode != "" && in.LLMMode != "offline" && !in.LLMAvailable {
		if in.ModelMissing {
			code, title, detail, cause, fixable = "model_missing", "本地模型未就绪", "本地推理模型缺失或尚未下载完成，AI 暂时无法决策。", "model_missing", true
		} else {
			code, title, detail, cause, fixable = "llm_unavailable", "AI 模型不可用", "当前没有可用的大模型提供方，AI 无法生成决策。", "llm_unavailable", true
		}
	}
	if code == "ok" && in.Status == domain.StatusError {
		code, title, detail, cause = "unknown", "发生未知错误", "实例处于错误状态，但错误信息无法自动归类，请查看日志。", "internal"
	}

	if code == "ok" && in.Status == domain.StatusStopped {
		title = "实例已停止"
		detail = "实例当前未运行，启动后即可查看实时画面与诊断。"
	}

	switch code {
	case "external_control":
		res.Phase = "human_control"
	case "android_no_client", "android_ack_timeout", "android_action_failed", "capture_failed":
		res.Phase = "capture"
	case "llm_unavailable", "model_missing":
		res.Phase = "model"
	}

	res.OK = code == "ok"
	res.Code = code
	res.Title = title
	res.Detail = detail
	res.Cause = cause
	res.UserFixable = fixable
	res.Suggestions = diagnosticSuggestions(code)
	return res
}

func diagnosticChecks(in diagInput) []diagCheck {
	checks := []diagCheck{
		{ID: "instance", Label: "实例状态", OK: in.Status != domain.StatusError, Detail: "当前状态：" + string(in.Status)},
	}
	isAndroid := in.Source == "android"
	if isAndroid {
		checks = append(checks, diagCheck{
			ID: "bridge", Label: "Android 连接", OK: in.BridgeConnected,
			Detail: boolDetail(in.BridgeConnected, "手机端已连接", "手机端未连接"),
		})
		framesOK := in.Frames > 0 && (in.LastFrameAgeMS < 0 || in.LastFrameAgeMS <= 5000)
		checks = append(checks, diagCheck{
			ID: "frames", Label: "实时画面", OK: framesOK,
			Detail: framesDetail(in.Frames, in.LastFrameAgeMS),
		})
		checks = append(checks, diagCheck{
			ID: "actions", Label: "操作回执", OK: in.BridgeErrors == 0,
			Detail: countDetail("操作错误", in.BridgeErrors),
		})
	} else {
		checks = append(checks,
			diagCheck{ID: "bridge", Label: "Android 连接", OK: true, Detail: "本实例使用本地采集源，不依赖 Android 桥"},
			diagCheck{ID: "frames", Label: "实时画面", OK: true, Detail: "本地采集源"},
			diagCheck{ID: "actions", Label: "操作回执", OK: in.BridgeErrors == 0, Detail: countDetail("操作错误", in.BridgeErrors)},
		)
	}
	modelOK := in.LLMMode == "" || in.LLMMode == "offline" || in.LLMAvailable
	modelDetail := "离线策略模式，无需大模型"
	if in.LLMMode != "" && in.LLMMode != "offline" {
		modelDetail = "提供方模式：" + in.LLMMode
		if !in.LLMAvailable {
			modelDetail += "，当前不可用"
		}
	}
	checks = append(checks,
		diagCheck{ID: "model", Label: "AI 模型", OK: modelOK, Detail: modelDetail},
		diagCheck{ID: "control", Label: "控制权", OK: !in.Human, Detail: boolDetail(in.Human, "人工接管中", "AI 自动控制")},
	)
	return checks
}

func diagnosticSuggestions(code string) []diagSuggestion {
	openPermissions := diagSuggestion{ID: "android_permissions", Label: "打开 Android 权限中心", Detail: "授予无障碍、悬浮窗和屏幕录制权限", Action: "open_permissions"}
	confirmCapture := diagSuggestion{ID: "android_capture", Label: "确认无障碍/悬浮窗/录屏", Detail: "确认三项权限均已开启，并回到当前实例", Action: "start_capture"}
	confirmConnection := diagSuggestion{ID: "android_connection", Label: "确认激活实例与 WS", Detail: "确认手机端连接的是当前激活实例，且 WebSocket 未断开", Action: "start_capture"}
	retry := diagSuggestion{ID: "retry", Label: "重试", Detail: "重新启动实例并重新建立画面通道", Action: "retry_start"}
	openAI := diagSuggestion{ID: "open_ai", Label: "配置 AI 模型", Detail: "打开 AI 设置，选择可用的 API 或本地模型", Action: "open_ai"}
	release := diagSuggestion{ID: "release_human", Label: "交还控制", Detail: "结束人工接管，立即恢复 AI 自动决策", Action: "release_human"}
	openLogs := diagSuggestion{ID: "open_logs", Label: "查看日志", Detail: "打开实例日志定位详细错误", Action: "open_logs"}

	switch code {
	case "external_control":
		return []diagSuggestion{release}
	case "android_no_client":
		return []diagSuggestion{openPermissions, confirmConnection, retry}
	case "android_ack_timeout":
		return []diagSuggestion{confirmCapture, retry, openLogs}
	case "android_action_failed":
		return []diagSuggestion{openPermissions, retry, openLogs}
	case "capture_failed":
		return []diagSuggestion{confirmCapture, openPermissions, retry}
	case "llm_unavailable":
		return []diagSuggestion{openAI, openLogs}
	case "model_missing":
		return []diagSuggestion{openAI, retry}
	case "unknown":
		return []diagSuggestion{openLogs}
	default:
		return []diagSuggestion{}
	}
}

func boolDetail(v bool, yes, no string) string {
	if v {
		return yes
	}
	return no
}

func countDetail(label string, n int64) string {
	if n == 0 {
		return label + "：0"
	}
	return label + "：" + itoa(n)
}

func framesDetail(frames, ageMS int64) string {
	if frames == 0 {
		return "尚未收到画面"
	}
	if ageMS < 0 {
		return "已收到 " + itoa(frames) + " 帧"
	}
	return "已收到 " + itoa(frames) + " 帧，最近一帧 " + itoa(ageMS) + "ms 前"
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// instanceDiagnostics reports classifying facts about one instance. It never
// returns raw frames and never calls the bridge while holding its lock.
func (s *Server) instanceDiagnostics(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	view, err := s.mgr.Get(id)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	b := s.bridgeDiagnostics(id)
	in := diagInput{
		Status:          view.Status,
		Source:          view.SourceKind,
		LastError:       view.Stats.LastError,
		Human:           view.Human || b.Human,
		BridgeConnected: b.Connected,
		Frames:          b.Frames,
		LastFrameAgeMS:  b.LastFrameAgeMS,
		BridgeErrors:    b.Errors,
		InstanceActions: view.Stats.Actions,
	}
	if router := s.mgr.Router(); router != nil {
		in.LLMMode = router.Mode()
		if in.LLMMode != "" && in.LLMMode != "offline" {
			ctx, cancel := context.WithTimeout(r.Context(), 1500*time.Millisecond)
			in.LLMAvailable = router.Available(ctx)
			cancel()
		}
		if in.LLMMode != "" && in.LLMMode != "offline" {
			if st, ok := s.mgr.LocalStatus(); ok {
				in.ModelMissing = localModelMissing(st)
			}
		}
	}
	res := classifyDiagnostic(in)
	res.Bridge = b
	res.Instance = diagInstance{
		ID:        view.ID,
		Status:    string(view.Status),
		Profile:   view.Profile,
		Source:    view.SourceKind,
		Actions:   view.Stats.Actions,
		Errors:    view.Stats.Errors,
		Decisions: view.Stats.Decisions,
		Wins:      view.Stats.Wins,
		Losses:    view.Stats.Losses,
		Progress:  view.Stats.Progress,
	}
	if !view.FrameAt.IsZero() {
		at := view.FrameAt
		res.Instance.FrameAt = &at
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) bridgeDiagnostics(id string) diagBridge {
	out := diagBridge{LastFrameAgeMS: -1}
	sess, ok := s.mgr.AndroidSession(id)
	if !ok || sess == nil {
		return out
	}
	info := sess.Info()
	out.Connected, _ = info["connected"].(bool)
	out.Client, _ = info["client"].(string)
	out.Device, _ = info["device"].(string)
	out.Frames, _ = info["frames"].(int64)
	out.Actions, _ = info["actions"].(int64)
	out.Errors, _ = info["errors"].(int64)
	out.LastFrameAgeMS, _ = info["last_frame_age_ms"].(int64)
	out.Human, _ = info["human"].(bool)
	return out
}

func localModelMissing(st localruntime.Status) bool {
	if st.Server.Running || st.Server.Model != "" {
		return false
	}
	for _, m := range st.Models {
		if m.Present {
			return false
		}
	}
	for _, m := range st.Catalog {
		if m.Present {
			return false
		}
	}
	return len(st.Models) > 0 || st.Error != ""
}

// instanceFrame serves the latest JPEG frame for a live preview.
func (s *Server) instanceFrame(w http.ResponseWriter, r *http.Request) {
	view, err := s.mgr.Get(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	if len(view.FrameJPEG) == 0 {
		writeError(w, http.StatusNotFound, "frame not ready")
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(view.FrameJPEG)
}

// releaseInstance force-returns control from a human to the AI. It clears the
// bridge's human window and resumes a paused runner.
func (s *Server) releaseInstance(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.mgr.Get(id); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	if sess, ok := s.mgr.AndroidSession(id); ok && sess != nil {
		sess.SetHuman(false, "user-released")
		sess.ClearHuman("user-released")
	}
	if err := s.mgr.Resume(id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	view, err := s.mgr.Get(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, view)
}
