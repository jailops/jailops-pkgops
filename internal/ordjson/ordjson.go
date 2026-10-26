// Package ordjson builds and parses JSON objects whose key order is preserved.
// pkg writes manifests in a fixed key order and pkgops reproduces it.
package ordjson

import (
	"bytes"
	"encoding/json"
	"errors"
)

// Obj is a JSON object with insertion-ordered keys.
type Obj struct {
	Keys []string
	Vals []json.RawMessage
}

// Set appends key with the JSON encoding of v (no HTML escaping).
func (o *Obj) Set(k string, v any) { o.Raw(k, Marshal(v)) }

// Raw appends key with an already encoded value.
func (o *Obj) Raw(k string, v json.RawMessage) {
	o.Keys = append(o.Keys, k)
	o.Vals = append(o.Vals, v)
}

// Clone returns a copy that can be appended to independently.
func (o *Obj) Clone() *Obj {
	return &Obj{Keys: append([]string(nil), o.Keys...), Vals: append([]json.RawMessage(nil), o.Vals...)}
}

// Bytes returns the compact encoding.
func (o *Obj) Bytes() []byte {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, k := range o.Keys {
		if i > 0 {
			b.WriteByte(',')
		}
		b.Write(Marshal(k))
		b.WriteByte(':')
		b.Write(o.Vals[i])
	}
	b.WriteByte('}')
	return b.Bytes()
}

// Marshal encodes v compactly without escaping <, > and &, as pkg does.
// It panics on unencodable values; callers pass strings, numbers and maps.
func Marshal(v any) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		panic(err)
	}
	return bytes.TrimRight(b.Bytes(), "\n")
}

// Parse decodes the top level of a JSON object, keeping key order and the raw
// bytes of every value.
func Parse(data []byte) (*Obj, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	t, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := t.(json.Delim); !ok || d != '{' {
		return nil, errors.New("not a JSON object")
	}
	o := &Obj{}
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return nil, err
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, err
		}
		o.Keys = append(o.Keys, kt.(string))
		o.Vals = append(o.Vals, raw)
	}
	return o, nil
}
