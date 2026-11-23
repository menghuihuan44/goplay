-- Mock dungeon profile: a deterministic training ground for the agent.
-- The Lua layer owns game semantics; perception owns pixels and the
-- simulator owns game rules. This keeps the loop honest: the agent only
-- receives screenshots and emits inputs.
local p = {}

p.id = "mock_dungeon"
p.name = "Mock Dungeon"
p.description = "Deterministic room-by-room dungeon used for learning and evaluation."
p.games = { "mock_dungeon" }

p.rules = {
  required_tags = { "advance" },
  notes = {
    "Rooms must be cleared before advancing.",
    "Advancing is a required tag: this game is about progression.",
  },
}

local function n(v, d)
  if type(v) == "number" then return v end
  return d or 0
end

function p.observe(s)
  local hp = n(s.hp, 100)
  local max_hp = n(s.max_hp, 100)
  local enemy_hp = n(s.enemy_hp, 0)
  local room = n(s.room, 1)
  local phase = "combat"
  if s.dead then
    phase = "dead"
  elseif s.won then
    phase = "won"
  elseif enemy_hp <= 0 then
    phase = "clear"
  end
  if not s.dead and not s.won and hp <= max_hp * 0.35 then
    phase = phase .. "_low"
  end
  if room >= 10 then phase = phase .. "_boss" end

  local tags = { "room" .. room, phase }
  if s.potions and s.potions > 0 then table.insert(tags, "has_potion") end
  if enemy_hp <= 0 then table.insert(tags, "enemy_dead") end
  if s.boss then table.insert(tags, "boss") end

  return {
    summary = string.format("room %d hp %d/%d enemy %d potions %d",
      room, hp, max_hp, enemy_hp, n(s.potions, 0)),
    values = {
      hp = hp,
      max_hp = max_hp,
      enemy_hp = enemy_hp,
      enemy_max_hp = n(s.enemy_max_hp, 1),
      potions = n(s.potions, 0),
      room = room,
      gold = n(s.gold, 0),
    },
    text = { phase = phase },
    tags = tags,
    confidence = 1.0,
  }
end

function p.actions(s)
  local out = {}
  local enemy_hp = n(s.enemy_hp, 0)
  local hp = n(s.hp, 100)
  local max_hp = n(s.max_hp, 100)
  local potions = n(s.potions, 0)
  local foe = "enemy"
  if s.boss then foe = "boss" end

  if s.dead or s.won then
    return out
  end

  if enemy_hp > 0 then
    local combat_tags = { "combat", "damage" }
    if s.boss then table.insert(combat_tags, "boss") end
    local guard_tags = { "combat", "guard" }
    if s.boss then table.insert(guard_tags, "boss") end
    table.insert(out, {
      kind = "attack", target = foe, tags = combat_tags,
      risk = 0.35, reason = "enemy alive",
      input = { type = "key", key = "a" },
    })
    table.insert(out, {
      kind = "defend", target = foe, tags = guard_tags,
      risk = 0.15, reason = "reduce incoming damage",
      input = { type = "key", key = "d" },
    })
  else
    table.insert(out, {
      kind = "advance", target = "next_room", tags = { "advance", "progress" },
      risk = 0.2, reason = "room cleared",
      input = { type = "key", key = "w" },
    })
    if hp < max_hp then
      table.insert(out, {
        kind = "rest", target = "self", tags = { "heal", "recover" },
        risk = 0.05, reason = "recover between rooms",
        input = { type = "key", key = "r" },
      })
    end
  end

  if potions > 0 and hp <= max_hp * 0.6 then
    table.insert(out, {
      kind = "heal", target = "self", tags = { "heal", "potion" },
      risk = 0.05, reason = "low health",
      input = { type = "key", key = "h" },
    })
  end
  return out
end

function p.interpret(before, after)
  local reward = 0
  local delta = 0
  local b = before or {}
  local a = after or {}

  local hp_delta = n(a.hp, 0) - n(b.hp, 0)
  if hp_delta > 0 then
    reward = reward + hp_delta * 0.10
  elseif hp_delta < 0 then
    reward = reward + hp_delta * 0.06
  end
  local dmg = n(b.enemy_hp, 0) - n(a.enemy_hp, 0)
  if dmg > 0 then reward = reward + dmg * 0.05 end

  if n(a.room, 1) > n(b.room, 1) then reward = reward + 10 end
  if a.dead and not b.dead then reward = reward - 5 end
  if a.won and not b.won then reward = reward + 50 end
  delta = n(a.room, 1) - n(b.room, 1)

  return {
    reward = reward,
    delta = delta,
    terminal = (a.dead == true) or (a.won == true),
    won = a.won == true,
    note = string.format("room %d hp %d enemy %d",
      n(a.room, 1), n(a.hp, 0), n(a.enemy_hp, 0)),
  }
end

return p
