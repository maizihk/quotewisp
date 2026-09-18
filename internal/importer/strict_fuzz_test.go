package importer

import (
	"context"
	"io"
	"testing"
	"unicode/utf8"
)

const strictPrefix = `{"categories":[{"code":"x","name":"x"}],"sentences":[{"uuid":"75a45fd4-4f2f-45eb-80cb-6f0a7bcdfaf2","category":"x","content":"`
const strictSuffix = `"}]}`

type fixedChunkReader struct {
	data []byte
	size int
}

func (r *fixedChunkReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	n := r.size
	if n < 1 {
		n = 1
	}
	if n > len(p) {
		n = len(p)
	}
	if n > len(r.data) {
		n = len(r.data)
	}
	copy(p, r.data[:n])
	r.data = r.data[n:]
	return n, nil
}

func strictDocument(encoded []byte) []byte {
	out := make([]byte, 0, len(strictPrefix)+len(encoded)+len(strictSuffix))
	out = append(out, strictPrefix...)
	out = append(out, encoded...)
	return append(out, strictSuffix...)
}

func TestStrictUnicodeBoundaries(t *testing.T) {
	tests := []struct {
		name    string
		encoded []byte
		valid   bool
	}{
		{"invalid UTF-8", []byte{0xff}, false},
		{"high without low", []byte(`\ud800`), false},
		{"high high low", []byte(`\ud800\ud801\udc00`), false},
		{"high then raw Unicode", []byte(`\ud800中\udc00`), false},
		{"fixed surrogate pair", []byte(`\uD83D\uDE00`), true},
		{"literal replacement rune", []byte("�"), true},
		{"escaped replacement rune", []byte(`\uFFFD`), true},
		{"escaped backslash is not surrogate", []byte(`\\ud800`), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for _, size := range []int{1, 2, 3, 7, 64} {
				d, e := Parse(context.Background(), &fixedChunkReader{data: strictDocument(tc.encoded), size: size})
				if tc.valid && e != nil {
					t.Fatalf("chunk %d rejected: %v", size, e)
				}
				if !tc.valid && e == nil {
					t.Fatalf("chunk %d accepted: %+v", size, d)
				}
			}
		})
	}
}

func FuzzStrictJSONStringUnicode(f *testing.F) {
	for _, s := range [][]byte{[]byte("ascii"), []byte("中"), []byte(`\ud83d\ude00`), []byte(`\ud800`), []byte(`\\ud800`), []byte{0xff}, []byte(`\ud800中\udc00`)} {
		f.Add(s, uint8(1))
	}
	f.Fuzz(func(t *testing.T, encoded []byte, chunk uint8) {
		if len(encoded) > 4096 {
			t.Skip()
		}
		oracle := strictUnicodeEncodingOK(encoded)
		d, e := Parse(context.Background(), &fixedChunkReader{data: strictDocument(encoded), size: int(chunk%31) + 1})
		if !oracle && e == nil {
			t.Fatalf("accepted invalid Unicode encoding %q", encoded)
		}
		if e == nil {
			if len(d.Sentences) != 1 || !utf8.ValidString(d.Sentences[0].Content) {
				t.Fatalf("invalid successful result")
			}
		}
	})
}

// strictUnicodeEncodingOK is an independent oracle for Unicode validity inside
// one JSON string. Other JSON syntax errors may still make Parse reject input.
func strictUnicodeEncodingOK(in []byte) bool {
	for i := 0; i < len(in); {
		if in[i] == '\\' {
			if i+1 >= len(in) {
				return true
			}
			if in[i+1] != 'u' {
				i += 2
				continue
			}
			u, ok := fourHex(in, i+2)
			if !ok {
				return true
			}
			i += 6
			if u >= 0xdc00 && u <= 0xdfff {
				return false
			}
			if u >= 0xd800 && u <= 0xdbff {
				if i+6 > len(in) || in[i] != '\\' || in[i+1] != 'u' {
					return false
				}
				low, ok := fourHex(in, i+2)
				if !ok || low < 0xdc00 || low > 0xdfff {
					return false
				}
				i += 6
			}
			continue
		}
		r, n := utf8.DecodeRune(in[i:])
		if r == utf8.RuneError && n == 1 {
			return false
		}
		i += n
	}
	return true
}
func fourHex(in []byte, start int) (uint16, bool) {
	if start+4 > len(in) {
		return 0, false
	}
	var v uint16
	for _, c := range in[start : start+4] {
		x, ok := hexValue(c)
		if !ok {
			return 0, false
		}
		v = v<<4 | x
	}
	return v, true
}
