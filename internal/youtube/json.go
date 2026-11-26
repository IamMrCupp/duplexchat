package youtube

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
)

// Obj is a JSON object that remembers its key order. multichat walks
// InnerTube responses with Object.values, so which continuation wins
// depends on order, and Go maps don't keep it.
type Obj struct {
	Keys []string
	M    map[string]any
}

// parse decodes JSON into nil, bool, float64, string, []any or *Obj.
func parse(b []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	v, err := value(dec)
	if err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, fmt.Errorf("trailing data")
	}
	return v, nil
}

func value(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			o := &Obj{M: map[string]any{}}
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				k := kt.(string)
				v, err := value(dec)
				if err != nil {
					return nil, err
				}
				if _, dup := o.M[k]; !dup {
					o.Keys = append(o.Keys, k)
				}
				o.M[k] = v
			}
			_, err := dec.Token()
			return o, err
		case '[':
			l := []any{}
			for dec.More() {
				v, err := value(dec)
				if err != nil {
					return nil, err
				}
				l = append(l, v)
			}
			_, err := dec.Token()
			return l, err
		}
	case json.Number:
		f, err := strconv.ParseFloat(string(t), 64)
		return f, err
	}
	return tok, nil
}

// get walks a path of keys and indices; nil when anything's missing.
func get(v any, path ...any) any {
	for _, p := range path {
		switch k := p.(type) {
		case string:
			o, ok := v.(*Obj)
			if !ok {
				return nil
			}
			v = o.M[k]
		case int:
			l, ok := v.([]any)
			if !ok || k < 0 || k >= len(l) {
				return nil
			}
			v = l[k]
		}
		if v == nil {
			return nil
		}
	}
	return v
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func list(v any) []any {
	l, _ := v.([]any)
	return l
}
