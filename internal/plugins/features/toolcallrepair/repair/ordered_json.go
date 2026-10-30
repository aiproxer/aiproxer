package repair

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

type orderedObject struct {
	keys   []string
	values map[string]any
}

func (o orderedObject) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, k := range o.keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		kb, err := json.Marshal(k)
		if err != nil {
			return nil, err
		}
		buf.Write(kb)
		buf.WriteByte(':')
		vb, err := json.Marshal(o.values[k])
		if err != nil {
			return nil, err
		}
		buf.Write(vb)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

func parseOrderedJSON(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	v, err := decodeOrderedValue(dec)
	if err != nil {
		return nil, err
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, fmt.Errorf("trailing json")
		}
		return nil, err
	}
	return v, nil
}

func decodeOrderedValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			obj := orderedObject{values: make(map[string]any)}
			for dec.More() {
				keyTok, err := dec.Token()
				if err != nil {
					return nil, err
				}
				key, ok := keyTok.(string)
				if !ok {
					return nil, fmt.Errorf("object key type %T", keyTok)
				}
				val, err := decodeOrderedValue(dec)
				if err != nil {
					return nil, err
				}
				if _, exists := obj.values[key]; exists {
					return nil, fmt.Errorf("duplicate key %q", key)
				}
				obj.keys = append(obj.keys, key)
				obj.values[key] = val
			}
			end, err := dec.Token()
			if err != nil {
				return nil, err
			}
			if end != json.Delim('}') {
				return nil, fmt.Errorf("expected }")
			}
			return obj, nil
		case '[':
			arr := make([]any, 0, 4)
			for dec.More() {
				val, err := decodeOrderedValue(dec)
				if err != nil {
					return nil, err
				}
				arr = append(arr, val)
			}
			end, err := dec.Token()
			if err != nil {
				return nil, err
			}
			if end != json.Delim(']') {
				return nil, fmt.Errorf("expected ]")
			}
			return arr, nil
		default:
			return nil, fmt.Errorf("unexpected delim %v", t)
		}
	case string, bool, nil, json.Number, float64:
		return t, nil
	default:
		return nil, fmt.Errorf("unexpected token %T", tok)
	}
}

func objectFields(v any) (keys []string, values map[string]any, ok bool) {
	switch t := v.(type) {
	case orderedObject:
		return t.keys, t.values, true
	case map[string]any:
		keys = make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		return keys, t, true
	default:
		return nil, nil, false
	}
}
