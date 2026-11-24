-- Vertical shooter profile for Touhou-like games.
-- The Go ShmupAnalyzer supplies pixel-level player/bullet positions; this file
-- owns the game semantics (which keys mean shoot/focus/bomb) and turns the
-- analyzer's five safety scores into legal movement actions.
local p = {}

p.id = "touhou"
p.name = "Touhou Project (Vertical Shmup)"
p.description = "Arrow keys, Z shoot, Shift focus, X bomb. Pixel dodge scores come from the external screen analyzer."
p.games = { "th06", "th07", "th08", "th09", "th10", "th11", "th12", "th13", "th14", "th15", "th16", "th17", "th18" }

p.rules = {
  required_tags = {},
  notes = {
    "This is a vertical shooter: arrows move, Z shoots, Shift focuses, X bombs.",
    "Never click outside the playfield; the HUD is not a control.",
    "If the screen is a menu, Enter/Z confirm and Escape backs out.",
  },
}

local function n(v, d)
  if type(v) == "number" then return v end
  return d or 0
end

function p.observe(s)
  local phase = "playing"
  if type(s.text) == "table" and type(s.text.phase) == "string" then
    phase = s.text.phase
  end
  local bullets = n(s.bullets, 0)
  local danger = n(s.danger, 0)
  local px = n(s.player_x, 0.5)
  local py = n(s.player_y, 0.9)
  local tags = { phase, "shmup" }
  if danger > 0.55 then table.insert(tags, "danger") end
  if bullets == 0 then table.insert(tags, "no_bullets") end
  return {
    summary = string.format("shmup player=(%.2f,%.2f) bullets=%d danger=%.2f",
      px, py, bullets, danger),
    values = {
      player_x = px, player_y = py,
      bullets = bullets, danger = danger,
      safe_stay = n(s.safe_stay, 0.5), safe_left = n(s.safe_left, 0.5),
      safe_right = n(s.safe_right, 0.5), safe_up = n(s.safe_up, 0.5),
      safe_down = n(s.safe_down, 0.5),
    },
    text = { phase = phase },
    tags = tags,
    confidence = 0.75,
  }
end

local function move(kind, key, tag, safety, why)
  return {
    kind = kind, target = tag, tags = { "move", "dodge", tag },
    risk = 0.05, reason = why,
    params = { safety = safety },
    input = { type = "key", key = key, duration_ms = 110 },
  }
end

function p.actions(s)
  local out = {}
  local phase = "playing"
  if type(s.text) == "table" and type(s.text.phase) == "string" then
    phase = s.text.phase
  end
  if phase == "menu" then
    -- Touhou's title screen defaults to "Start". Random navigation kept the
    -- cursor moving forever, which looked like the AI "scrolling the menu but
    -- never playing". Only send confirm keys (plus a rare Escape to leave a
    -- wrong submenu); never navigate with arrows here.
    p.menuTick = (p.menuTick or 0) + 1
    local t = p.menuTick % 20
    if t < 12 then
      return { { kind = "confirm", target = "menu", tags = { "ui", "confirm" },
        risk = 0.05, reason = "confirm start with Z", input = { type = "key", key = "z", duration_ms = 40 } } }
    elseif t < 16 then
      return { { kind = "confirm_enter", target = "menu", tags = { "ui", "confirm" },
        risk = 0.05, reason = "confirm start with Enter", input = { type = "key", key = "enter", duration_ms = 40 } } }
    elseif t == 16 then
      return { { kind = "back", target = "menu", tags = { "ui", "back" },
        risk = 0.1, reason = "leave a wrong submenu", input = { type = "key", key = "esc", duration_ms = 40 } } }
    else
      return { { kind = "wait", target = "menu", tags = { "wait", "ui" },
        risk = 0.0, reason = "let the menu transition", input = { type = "wait", duration_ms = 120 } } }
    end
  end
  p.menuTick = 0

  table.insert(out, move("move_left", "left", "left", n(s.safe_left, 0.5), "dodge left based on bullet density"))
  table.insert(out, move("move_right", "right", "right", n(s.safe_right, 0.5), "dodge right based on bullet density"))
  table.insert(out, move("move_up", "up", "up", n(s.safe_up, 0.5), "dodge up based on bullet density"))
  table.insert(out, move("move_down", "down", "down", n(s.safe_down, 0.5), "dodge down based on bullet density"))
  table.insert(out, {
    kind = "shoot", target = "enemy", tags = { "combat", "damage" },
    risk = 0.1, reason = "keep shooting while dodging",
    input = { type = "key", key = "z", duration_ms = 160 },
  })
  table.insert(out, {
    kind = "focus", target = "self", tags = { "focus", "precision" },
    risk = 0.02, reason = "slow movement for precise dodging",
    input = { type = "key", key = "shift", duration_ms = 110 },
  })
  table.insert(out, {
    kind = "bomb", target = "self", tags = { "bomb", "survive" },
    risk = 0.4, reason = "clear bullets when trapped",
    input = { type = "key", key = "x", duration_ms = 45 },
  })
  table.insert(out, {
    kind = "wait", target = "self", tags = { "wait", "idle", "safe" },
    risk = 0.0, reason = "hold position",
    params = { safety = n(s.safe_stay, 0.5) },
    input = { type = "wait", duration_ms = 60 },
  })
  return out
end

function p.interpret(before, after)
  local b = before or {}
  local a = after or {}
  local dangerBefore = n(b.danger, 0)
  local dangerAfter = n(a.danger, 0)
  local reward = (dangerBefore - dangerAfter) * 3.0
  if n(a.bullets, 0) > 0 and n(b.bullets, 0) > 0 then reward = reward + 0.05 end
  return {
    reward = reward,
    delta = dangerBefore - dangerAfter,
    terminal = false,
    won = false,
    note = string.format("danger %.2f -> %.2f", dangerBefore, dangerAfter),
  }
end

return p
