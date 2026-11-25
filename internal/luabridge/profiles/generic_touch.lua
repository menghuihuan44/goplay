-- generic_touch: a game-agnostic profile for touch-only devices (Android).
-- It never assumes a keyboard. Actions are normalized screen coordinates.
local profile = {
  id = "generic_touch",
  name = "Generic Touch (Android)",
  description = "Touch-only fallback: taps a 3x3 grid, swipes, back and wait.",
  games = {"*"},
  rules = { required_tags = {}, forbidden_tags = {} },
}

local function num(v, default)
  if type(v) == "number" then return v end
  return default or 0
end

function profile.observe(s)
  local tags = {"generic", "touch"}
  if type(s.tags) == "table" then
    for _, t in ipairs(s.tags) do
      if #tags < 16 then tags[#tags+1] = t end
    end
  end
  local text = {}
  if type(s.text) == "table" then
    for k, v in pairs(s.text) do text[k] = v end
  end
  return {
    summary = s.summary or "unknown touch screen",
    values = s.values or {},
    text = text,
    tags = tags,
    confidence = num(s.confidence, 0.3),
  }
end

function profile.actions(s)
  local acts = {
    { kind = "wait", target = "screen", tags = {"idle"}, input = { type = "wait", duration_ms = 300 } },
    { kind = "back", target = "screen", tags = {"ui", "back"}, input = { type = "back" } },
  }
  local xs = {0.25, 0.5, 0.75}
  local ys = {0.30, 0.55, 0.80}
  for iy, y in ipairs(ys) do
    for ix, x in ipairs(xs) do
      acts[#acts+1] = {
        kind = "tap", target = string.format("screen_%d_%d", ix, iy),
        tags = {"ui", "select", string.format("cell_%d_%d", ix, iy)},
        risk = 0.15,
        input = { type = "tap", x = x, y = y },
      }
    end
  end
  acts[#acts+1] = { kind = "swipe", target = "screen_up", tags = {"ui", "scroll", "up"}, risk = 0.2, input = { type = "swipe", x = 0.5, y = 0.72, x2 = 0.5, y2 = 0.32, duration_ms = 260 } }
  acts[#acts+1] = { kind = "swipe", target = "screen_down", tags = {"ui", "scroll", "down"}, risk = 0.2, input = { type = "swipe", x = 0.5, y = 0.32, x2 = 0.5, y2 = 0.72, duration_ms = 260 } }
  acts[#acts+1] = { kind = "swipe", target = "screen_left", tags = {"ui", "scroll", "left"}, risk = 0.2, input = { type = "swipe", x = 0.72, y = 0.5, x2 = 0.32, y2 = 0.5, duration_ms = 260 } }
  acts[#acts+1] = { kind = "swipe", target = "screen_right", tags = {"ui", "scroll", "right"}, risk = 0.2, input = { type = "swipe", x = 0.32, y = 0.5, x2 = 0.72, y2 = 0.5, duration_ms = 260 } }
  return acts
end

-- The engine passes the flattened world map (values live at the top level),
-- the same shape generic.lua consumes. Reading only before.values made every
-- touch result look like zero progress, which is why touch play looked random.
function profile.interpret(before, after)
  local interp = { reward = 0, delta = 0, terminal = false, won = false, note = "touch frame delta" }
  local bv = type(before.values) == "table" and before.values or before
  local av = type(after.values) == "table" and after.values or after
  if type(bv) ~= "table" or type(av) ~= "table" then return interp end
  local sum = 0
  for k, v in pairs(av) do
    local b = bv[k]
    if type(v) == "number" and type(b) == "number" then
      sum = sum + (v - b)
    end
  end
  interp.delta = sum
  local scene = num(av.scene_change, 0)
  if scene > 0 then interp.reward = interp.reward + math.min(4, scene * 4) end
  local delta = num(av.frame_delta, 0)
  if delta > 0.02 then interp.reward = interp.reward + math.min(1, delta * 3) end
  if sum > 0 then interp.reward = interp.reward + math.min(2, sum * 0.04) end
  if interp.reward > 0.05 then
    interp.note = string.format("touch changed scene %.0f%% (frame %.0f%%)", scene * 100, delta * 100)
  else
    interp.note = "no meaningful touch change"
  end
  if av.won then interp.won = true; interp.reward = 20; interp.terminal = true end
  if av.dead then interp.reward = -5; interp.terminal = true end
  return interp
end

return profile
