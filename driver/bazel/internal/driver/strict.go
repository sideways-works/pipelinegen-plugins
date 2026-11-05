package driver

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"
)

type noun interface{ settingNoun() string }

// strictly decodes a step's with block into v after checking every key and
// type at every depth: a setting the driver ignored would go unseen.
func strictly(conf map[string]any, v any) error {
	data, err := json.Marshal(conf)
	if err != nil {
		return fmt.Errorf("%w: %w", errSettings, err)
	}

	var plain any

	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()

	if err := dec.Decode(&plain); err != nil {
		return fmt.Errorf("%w: %w", errSettings, err)
	}

	if plain == nil {
		plain = map[string]any{}
	}

	if err := walk(plain, reflect.TypeOf(v).Elem(), ""); err != nil {
		return err
	}

	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("%w: %w", errSettings, err)
	}

	return nil
}

func walk(val any, t reflect.Type, place string) error {
	// A key with no value would silently fall back to the default.
	if val == nil {
		return fmt.Errorf("%w: %s: has no value", errSettings, placeOr(place))
	}

	if t.Kind() == reflect.Pointer {
		return walk(val, t.Elem(), place)
	}

	switch t.Kind() {
	case reflect.Struct:
		m, ok := val.(map[string]any)
		if !ok {
			return mistyped(place, "a map of keys", val)
		}

		return walkStruct(m, t, place)
	case reflect.Slice:
		list, ok := val.([]any)
		if !ok {
			return mistyped(place, "a list", val)
		}

		for i, item := range list {
			if err := walk(item, t.Elem(), fmt.Sprintf("%s[%d]", place, i)); err != nil {
				return err
			}
		}
	case reflect.Map:
		m, ok := val.(map[string]any)
		if !ok {
			return mistyped(place, "a map", val)
		}

		for _, k := range slices.Sorted(maps.Keys(m)) {
			if err := walk(m[k], t.Elem(), fmt.Sprintf("%s[%s]", place, strconv.Quote(k))); err != nil {
				return err
			}
		}
	case reflect.String:
		if _, ok := val.(string); !ok {
			return mistyped(place, "a string", val)
		}
	case reflect.Bool:
		if _, ok := val.(bool); !ok {
			return mistyped(place, "true or false", val)
		}
	case reflect.Int, reflect.Int64:
		if !whole(val) {
			return mistyped(place, "a whole number", val)
		}
	default:
		return fmt.Errorf("%w: %s: the driver can't read a %s", errSettings, placeOr(place), t.Kind())
	}

	return nil
}

func walkStruct(m map[string]any, t reflect.Type, place string) error {
	fields := map[string]reflect.Type{}

	var keys []string

	for f := range t.Fields() {
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if !f.IsExported() || name == "" || name == "-" {
			continue
		}

		fields[name] = f.Type

		keys = append(keys, name)
	}

	for _, k := range slices.Sorted(maps.Keys(m)) {
		ft, ok := fields[k]
		if !ok {
			return fmt.Errorf("%w: %s: unknown key; the keys of %s are %s", errSettings, join(place, k), nounOf(t), strings.Join(keys, ", "))
		}

		if err := walk(m[k], ft, join(place, k)); err != nil {
			return err
		}
	}

	return nil
}

func nounOf(t reflect.Type) string {
	if n, ok := reflect.TypeAssert[noun](reflect.New(t)); ok {
		return n.settingNoun()
	}

	return "the settings"
}

func mistyped(place, want string, val any) error {
	return fmt.Errorf("%w: %s: takes %s, not %s", errSettings, placeOr(place), want, shown(val))
}

func shown(val any) string {
	switch v := val.(type) {
	case string:
		return strconv.Quote(v)
	case []any:
		return "a list"
	case map[string]any:
		return "a map"
	default:
		return fmt.Sprint(v)
	}
}

func whole(val any) bool {
	switch v := val.(type) {
	case json.Number:
		_, err := v.Int64()

		return err == nil
	default:
		return false
	}
}

func join(place, key string) string {
	if place == "" {
		return key
	}

	return place + "." + key
}

func placeOr(place string) string {
	if place == "" {
		return "the settings"
	}

	return place
}
