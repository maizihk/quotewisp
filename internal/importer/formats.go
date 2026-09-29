package importer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
)

const (
	FormatNative   = "native"
	FormatHitokoto = "hitokoto"
)

// ParseFormat parses one of the supported import formats. Hitokoto input is
// translated to the native format before validation, so both formats share
// the same normalization, length, deduplication, and conflict rules.
func ParseFormat(ctx context.Context, r io.Reader, format string) (Dataset, error) {
	switch format {
	case FormatNative:
		return Parse(ctx, r)
	case FormatHitokoto:
		return parseHitokoto(ctx, r)
	default:
		return Dataset{}, problem("invalid-format", "unsupported import format %q", format)
	}
}

func parseHitokoto(ctx context.Context, r io.Reader) (Dataset, error) {
	d := json.NewDecoder(newValidatingReader(ctx, r))
	d.UseNumber()
	t, err := d.Token()
	if err != nil {
		if ctx.Err() != nil {
			return Dataset{}, ctx.Err()
		}
		return Dataset{}, problem("invalid-json", "malformed JSON")
	}
	if delim, ok := t.(json.Delim); !ok || delim != '[' {
		return Dataset{}, problem("invalid-json", "top level must be an array")
	}

	types := make([]string, 0)
	seenTypes := map[string]bool{}
	sentences := make([]map[string]any, 0)
	for i := 0; d.More(); i++ {
		if err := ctx.Err(); err != nil {
			return Dataset{}, err
		}
		var raw json.RawMessage
		if err := d.Decode(&raw); err != nil {
			return Dataset{}, problem("invalid-json", "hitokoto[%d] is malformed JSON", i)
		}
		if err := validateJSONKeys(raw); err != nil {
			return Dataset{}, problem("duplicate-field", "hitokoto[%d]: %v", i, err)
		}
		var item map[string]json.RawMessage
		if err := json.Unmarshal(raw, &item); err != nil || item == nil {
			return Dataset{}, problem("validation", "hitokoto[%d] must be an object", i)
		}

		uuid, err := hitokotoRequiredString(item, i, "uuid")
		if err != nil {
			return Dataset{}, err
		}
		content, err := hitokotoRequiredString(item, i, "hitokoto")
		if err != nil {
			return Dataset{}, err
		}
		category, err := hitokotoRequiredString(item, i, "type")
		if err != nil {
			return Dataset{}, err
		}
		if !categoryRE.MatchString(category) {
			return Dataset{}, problem("validation", "hitokoto[%d].type is invalid", i)
		}
		source, err := hitokotoNullableString(item, i, "from")
		if err != nil {
			return Dataset{}, err
		}
		author, err := hitokotoNullableString(item, i, "from_who")
		if err != nil {
			return Dataset{}, err
		}
		if !seenTypes[category] {
			seenTypes[category] = true
			types = append(types, category)
		}
		sentences = append(sentences, map[string]any{
			"uuid": uuid, "category": category, "content": content,
			"source": source, "author": author,
		})
	}
	if _, err := d.Token(); err != nil {
		return Dataset{}, problem("invalid-json", "malformed JSON")
	}
	if _, err := d.Token(); err != io.EOF {
		return Dataset{}, problem("invalid-json", "trailing data or malformed JSON")
	}
	if err := ctx.Err(); err != nil {
		return Dataset{}, err
	}

	categories := make([]map[string]any, 0, len(types))
	for _, typ := range types {
		categories = append(categories, map[string]any{
			"code": typ, "name": "Hitokoto · " + typ, "sort_order": 0,
		})
	}
	native, err := json.Marshal(map[string]any{"categories": categories, "sentences": sentences})
	if err != nil {
		return Dataset{}, problem("invalid-json", "could not translate Hitokoto input")
	}
	return Parse(ctx, bytes.NewReader(native))
}

func hitokotoRequiredString(item map[string]json.RawMessage, i int, field string) (string, error) {
	raw, ok := item[field]
	if !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", problem("validation", "hitokoto[%d] requires %s", i, field)
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", problem("validation", "hitokoto[%d].%s must be a string", i, field)
	}
	return value, nil
}

func hitokotoNullableString(item map[string]json.RawMessage, i int, field string) (string, error) {
	raw, ok := item[field]
	if !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", problem("validation", "hitokoto[%d].%s must be a string or null", i, field)
	}
	return value, nil
}

// validateJSONKeys recursively rejects duplicate object members. This keeps
// the adapter strict even though unknown Hitokoto metadata is intentionally
// ignored.
func validateJSONKeys(raw []byte) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if err := validateJSONValue(d); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return fmt.Errorf("trailing data")
	}
	return nil
}

func validateJSONValue(d *json.Decoder) error {
	t, err := d.Token()
	if err != nil {
		return err
	}
	if delim, ok := t.(json.Delim); ok {
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				key, err := stringToken(d)
				if err != nil {
					return err
				}
				if seen[key] {
					return fmt.Errorf("duplicate field %q", key)
				}
				seen[key] = true
				if err := validateJSONValue(d); err != nil {
					return err
				}
			}
			_, err = d.Token()
			return err
		case '[':
			for d.More() {
				if err := validateJSONValue(d); err != nil {
					return err
				}
			}
			_, err = d.Token()
			return err
		}
	}
	return nil
}
