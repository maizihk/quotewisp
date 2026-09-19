package public

import (
	"testing"
	"unicode/utf8"

	"sentence-api/internal/web/store"
)

func FuzzValidateSubmission(f *testing.F) {
	data := &store.PublicData{
		Categories: []store.PublicCategory{{Code: "x", Name: "测试", Count: 1}},
	}
	f.Add("hello", "x", "src", "author", "nick", "contact@example.com", true)
	f.Add("", "", "", "", "", "", false)
	f.Fuzz(func(t *testing.T, content, category, source, author, nickname, contact string, agree bool) {
		if len(content) > 2048 || len(category) > 128 || len(source) > 512 || len(author) > 256 || len(nickname) > 64 || len(contact) > 512 {
			t.Skip()
		}
		errs := validateSubmission(submitValues{
			Content:  content,
			Category: category,
			Source:   source,
			Author:   author,
			Nickname: nickname,
			Contact:  contact,
			Agree:    agree,
		}, data)
		for field, msg := range errs {
			if field == "" || msg == "" {
				t.Fatalf("empty validation output field=%q msg=%q", field, msg)
			}
			if !utf8.ValidString(msg) {
				t.Fatalf("invalid UTF-8 message for %q", field)
			}
		}
	})
}
