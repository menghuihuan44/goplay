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
    key_action("press_enter", "enter", { "confirm", "ui" }, 0.1, "confirm a dialog or menu"),
    key_action("press_escape", "esc", { "back", "ui" }, 0.15, "back out of a menu"),
    key_action("press_space", "space", { "interact", "action" }, 0.2, "universal action / jump"),
    key_action("move_up", "w", { "move", "up" }, 0.2, "move up or forward"),
    key_action("move_down", "s", { "move", "down" }, 0.2, "move down or backward"),
    key_action("move_left", "a", { "move", "left" }, 0.2, "move left"),
    key_action("move_right", "d", { "move", "right" }, 0.2, "move right"),
    key_action("interact", "e", { "interact" }, 0.2, "context interaction"),
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
  local reward = 0
  local b = before or {}
  local a = after or {}
  if type(b.values) == "table" and type(a.values) == "table" then
    for k, v in pairs(a.values) do
      if type(v) == "number" and type(b.values[k]) == "number" then
        local d = v - b.values[k]
        if d > 0 then reward = reward + math.min(d, 10) * 0.05 end
      end
    end
  end
  if a.won == true and b.won ~= true then reward = reward + 20 end
  if a.dead == true and b.dead ~= true then reward = reward - 5 end
  return {
    reward = reward,
    delta = 0,
    terminal = (a.dead == true) or (a.won == true),
    won = a.won == true,
    note = "generic observation",
  }
end

return p
