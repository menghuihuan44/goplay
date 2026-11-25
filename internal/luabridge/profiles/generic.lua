-- Generic profile for games the agent has never seen.
-- It emits only universally safe low-level actions; the LLM policy proposes
-- the real intent, and the action validator is the last line of defence.
local p = {}

p.id = "generic"
p.name = "Generic Unfamiliar Game"
p.description = "Fallback profile: observe everything, act conservatively, learn from feedback."
p.games = {}

p.rules = {
  required_tags = {},
  notes = {
    "Unknown game: prefer observation and low-risk universal inputs.",
    "Never send inputs outside the whitelist validated by the Go layer.",
  },
}

function p.observe(s)
  local values = {}
  local texts = {}
  local tags = { "generic" }
  if type(s.tags) == "table" then
    for _, t in ipairs(s.tags) do
      if type(t) == "string" and #t > 0 and #tags < 16 then table.insert(tags, t) end
    end
  end
  local parts = {}
  if type(s.values) == "table" then
    for k, v in pairs(s.values) do
      if type(v) == "number" then values[k] = v end
    end
  end
  if type(s.text) == "table" then
    for k, v in pairs(s.text) do
      if type(v) == "string" and #v > 0 then
        texts[k] = v
        table.insert(parts, k .. "=" .. v)
      end
    end
  end
  if s.summary and #s.summary > 0 then table.insert(parts, s.summary) end
  return {
    summary = table.concat(parts, " | "),
    values = values,
    text = texts,
    tags = tags,
    confidence = 0.5,
  }
end

local function key_action(kind, key, tags, risk, why)
  return { kind = kind, target = "game", tags = tags, risk = risk,
    reason = why, input = { type = "key", key = key } }
end

function p.actions(s)
  local out = {
    { kind = "wait", target = "self", tags = { "wait", "safe" }, risk = 0.0,
      reason = "observe before acting", input = { type = "wait", duration_ms = 300 } },
    -- Escape is not offered for unknown games: it can quit or discard progress.
    -- The user can still inject it explicitly as a directive.
    key_action("press_enter", "enter", { "confirm", "ui" }, 0.1, "confirm a dialog or menu"),
    key_action("press_space", "space", { "interact", "action" }, 0.2, "universal action / jump"),
    key_action("move_up", "w", { "move", "up" }, 0.2, "move up or forward"),
    key_action("move_down", "s", { "move", "down" }, 0.2, "move down or backward"),
    key_action("move_left", "a", { "move", "left" }, 0.2, "move left"),
    key_action("move_right", "d", { "move", "right" }, 0.2, "move right"),
    key_action("interact", "e", { "interact" }, 0.2, "context interaction"),
    key_action("press_z", "z", { "action", "primary" }, 0.2, "primary attack / confirm"),
    key_action("press_x", "x", { "action", "secondary" }, 0.25, "secondary action"),
    key_action("press_shift", "shift", { "modifier", "focus" }, 0.15, "focus or slow movement"),
    key_action("arrow_up", "up", { "move", "up" }, 0.2, "move up"),
    key_action("arrow_down", "down", { "move", "down" }, 0.2, "move down"),
    key_action("arrow_left", "left", { "move", "left" }, 0.2, "move left"),
    key_action("arrow_right", "right", { "move", "right" }, 0.2, "move right"),
  }
  -- A coarse normalized grid lets a vision policy choose somewhere to click
  -- without inventing raw screen coordinates. The validator still checks [0,1].
  local xs = { 0.25, 0.5, 0.75 }
  local ys = { 0.3, 0.55, 0.8 }
  for iy, y in ipairs(ys) do
    for ix, x in ipairs(xs) do
      table.insert(out, {
        kind = "click", target = string.format("screen_%d_%d", ix, iy),
        tags = { "select", "ui", string.format("cell_%d_%d", ix, iy) },
        risk = 0.15, reason = "click a visible UI target",
        input = { type = "click", x = x, y = y },
      })
    end
  end
  return out
end

function p.interpret(before, after)
  local b = before or {}
  local a = after or {}
  local reward = 0
  local notes = {}

  -- Interpret receives the raw world maps (flat), not the nested observe
  -- tables. Reading before.values here silently returned nil and made every
  -- real game action score zero even when the screen visibly changed.
  local bv = type(b.values) == "table" and b.values or b
  local av = type(a.values) == "table" and a.values or a
  local beforeDelta = tonumber(bv.frame_delta) or 0
  local afterDelta = tonumber(av.frame_delta) or 0
  local sceneChange = tonumber(av.scene_change) or 0
  local sceneChanged = av.scene_changed == true or (type(av.scene_changed) == "number" and av.scene_changed > 0.5)
    or sceneChange >= 0.5

  -- A real screen transition is the strongest generic evidence that the last
  -- input did something meaningful. A frozen screen is not progress.
  if sceneChanged then
    reward = reward + 1.0
    notes[#notes + 1] = string.format("scene changed %.0f%%", sceneChange * 100)
  elseif sceneChange > 0.02 then
    reward = reward + math.min(sceneChange * 4, 0.3)
    notes[#notes + 1] = string.format("picture moved %.0f%%", sceneChange * 100)
  end
  if beforeDelta < 0.010 and afterDelta > 0.020 then
    reward = reward + 0.25
    notes[#notes + 1] = "screen woke up"
  end

  -- Only semantically meaningful counters count. Brightness, width, edge and
  -- frame-delta are observations, not rewards, or every action looks positive.
  local positive = {
    score = 0.02, points = 0.02, gold = 0.10, coins = 0.10,
    hp = 0.10, health = 0.10, lives = 1.0, power = 0.08,
    progress = 2.0, level = 1.0, room = 0.30, wave = 0.30,
    kills = 0.05, enemies = 0.20, distance = 0.02,
  }
  local negative = { hp = 0.10, health = 0.10, lives = 1.5 }
  for k, v in pairs(av) do
    if type(v) == "number" and type(bv[k]) == "number" then
      local d = v - bv[k]
      if d > 0 and positive[k] then
        reward = reward + math.min(d * positive[k], 2.0)
        notes[#notes + 1] = string.format("%s +%.3g", k, d)
      elseif d < 0 and negative[k] then
        reward = reward - math.min(-d * negative[k], 2.0)
        notes[#notes + 1] = string.format("%s %.3g", k, d)
      end
    end
  end

  if a.won == true and b.won ~= true then reward = reward + 20 end
  if a.dead == true and b.dead ~= true then reward = reward - 5 end
  return {
    reward = reward,
    delta = sceneChange,
    terminal = (a.dead == true) or (a.won == true),
    won = a.won == true,
    note = #notes > 0 and table.concat(notes, ", ") or "no meaningful change",
  }
end

return p
