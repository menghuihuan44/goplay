package luabridge

import (
	"fmt"
	"sort"

	lua "github.com/yuin/gopher-lua"
)

func toLua(L *lua.LState, v any) lua.LValue {
	switch x := v.(type) {
	case nil:
		return lua.LNil
	case bool:
		return lua.LBool(x)
	case string:
		return lua.LString(x)
	case float64:
		return lua.LNumber(x)
	case float32:
		return lua.LNumber(x)
	case int:
		return lua.LNumber(x)
	case int64:
		return lua.LNumber(x)
	case int32:
		return lua.LNumber(x)
	case []string:
		t := L.NewTable()
		for _, s := range x {
			t.Append(lua.LString(s))
		}
		return t
	case []any:
		t := L.NewTable()
		for _, e := range x {
			t.Append(toLua(L, e))
		}
		return t
	case map[string]string:
		t := L.NewTable()
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			t.RawSetString(k, lua.LString(x[k]))
		}
		return t
	case map[string]float64:
		t := L.NewTable()
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			t.RawSetString(k, lua.LNumber(x[k]))
		}
		return t
	case map[string]any:
		t := L.NewTable()
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			t.RawSetString(k, toLua(L, x[k]))
		}
		return t
	default:
		return lua.LString(fmt.Sprint(x))
	}
}

// fromLua converts tables to map[string]any or []any. Lua arrays are detected
// by requiring every key to be a positive integer.
func fromLua(v lua.LValue) any {
	switch x := v.(type) {
	case *lua.LNilType:
		return nil
	case lua.LBool:
		return bool(x)
	case lua.LString:
		return string(x)
	case lua.LNumber:
		return float64(x)
	case *lua.LTable:
		n := x.Len()
		isArray := n > 0
		count := 0
		x.ForEach(func(k, _ lua.LValue) {
			count++
			if kn, ok := k.(lua.LNumber); !ok || float64(kn) < 1 || float64(kn) > float64(n) || float64(kn) != float64(int(kn)) {
				isArray = false
			}
		})
		if isArray && count == n {
			out := make([]any, 0, n)
			for i := 1; i <= n; i++ {
				out = append(out, fromLua(x.RawGetInt(i)))
			}
			return out
		}
		out := map[string]any{}
		x.ForEach(func(k, val lua.LValue) {
			key := ""
			switch kk := k.(type) {
			case lua.LString:
				key = string(kk)
			case lua.LNumber:
				key = fmt.Sprintf("%g", float64(kk))
			default:
				key = k.String()
			}
			out[key] = fromLua(val)
		})
		return out
	default:
		return nil
	}
}

func tableField(t *lua.LTable, name string) lua.LValue {
	return t.RawGetString(name)
}

func asString(v lua.LValue) string {
	if s, ok := v.(lua.LString); ok {
		return string(s)
	}
	return ""
}

func asFloat(v lua.LValue) float64 {
	if n, ok := v.(lua.LNumber); ok {
		return float64(n)
	}
	return 0
}

func asBool(v lua.LValue) bool {
	if b, ok := v.(lua.LBool); ok {
		return bool(b)
	}
	return false
}

func stringSlice(v lua.LValue) []string {
	t, ok := v.(*lua.LTable)
	if !ok {
		return nil
	}
	var out []string
	t.ForEach(func(_, val lua.LValue) {
		if s := asString(val); s != "" {
			out = append(out, s)
		}
	})
	return out
}

func mapAny(v lua.LValue) map[string]any {
	m, _ := fromLua(v).(map[string]any)
	return m
}
