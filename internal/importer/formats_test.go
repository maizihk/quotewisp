package importer

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestParseFormatHitokotoMapsFieldsAndRecomputesLength(t *testing.T) {
	raw := `[{"uuid":"75A45FD4-4F2F-45EB-80CB-6F0A7BCDFAF2","hitokoto":"你好😀","type":"a_type","from":"来源","from_who":null,"id":12,"length":999,"meta":{"ignored":true}}]`
	d, err := ParseFormat(context.Background(), strings.NewReader(raw), FormatHitokoto)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Categories) != 1 || d.Categories[0] != (Category{Code: "a_type", Name: "Hitokoto · a_type"}) {
		t.Fatalf("unexpected categories: %+v", d.Categories)
	}
	if len(d.Sentences) != 1 {
		t.Fatalf("unexpected sentences: %+v", d.Sentences)
	}
	s := d.Sentences[0]
	if s.UUID != "75a45fd4-4f2f-45eb-80cb-6f0a7bcdfaf2" {
		t.Fatalf("unexpected UUID: %q", s.UUID)
	}
	if s.Content != "你好😀" || s.Category != "a_type" || s.Source != "来源" || s.Author != "" || s.Length != uint16(utf8.RuneCountInString("你好😀")) {
		t.Fatalf("unexpected sentence: %+v", s)
	}
}

func TestParseFormatHitokotoDuplicateAndConflictUseNativeRules(t *testing.T) {
	item := `{"uuid":"75a45fd4-4f2f-45eb-80cb-6f0a7bcdfaf2","hitokoto":"x","type":"x","from":null,"from_who":null}`
	d, err := ParseFormat(context.Background(), strings.NewReader("["+item+","+item+"]"), FormatHitokoto)
	if err != nil {
		t.Fatal(err)
	}
	if d.InputCount != 2 || d.DeduplicatedCount != 1 || len(d.Sentences) != 1 {
		t.Fatalf("unexpected counts: %+v", d)
	}
	conflict := strings.Replace("["+item+","+item+"]", `"hitokoto":"x"`, `"hitokoto":"y"`, 1)
	if _, err := ParseFormat(context.Background(), strings.NewReader(conflict), FormatHitokoto); err == nil {
		t.Fatal("expected duplicate UUID conflict")
	}
}

func TestParseFormatHitokotoRejectsMalformedCoreAndDuplicateKeys(t *testing.T) {
	tests := map[string]string{
		"root object":     `{}`,
		"trailing":        `[{"uuid":"75a45fd4-4f2f-45eb-80cb-6f0a7bcdfaf2","hitokoto":"x","type":"x"}] {}`,
		"duplicate core":  `[{"uuid":"75a45fd4-4f2f-45eb-80cb-6f0a7bcdfaf2","uuid":"75a45fd4-4f2f-45eb-80cb-6f0a7bcdfaf2","hitokoto":"x","type":"x"}]`,
		"duplicate extra": `[{"uuid":"75a45fd4-4f2f-45eb-80cb-6f0a7bcdfaf2","hitokoto":"x","type":"x","meta":{"a":1,"a":2}}]`,
		"missing type":    `[{"uuid":"75a45fd4-4f2f-45eb-80cb-6f0a7bcdfaf2","hitokoto":"x"}]`,
		"null content":    `[{"uuid":"75a45fd4-4f2f-45eb-80cb-6f0a7bcdfaf2","hitokoto":null,"type":"x"}]`,
		"bad type":        `[{"uuid":"75a45fd4-4f2f-45eb-80cb-6f0a7bcdfaf2","hitokoto":"x","type":"Not Valid"}]`,
		"bad optional":    `[{"uuid":"75a45fd4-4f2f-45eb-80cb-6f0a7bcdfaf2","hitokoto":"x","type":"x","from":3}]`,
		"bad utf8":        "[{\"uuid\":\"75a45fd4-4f2f-45eb-80cb-6f0a7bcdfaf2\",\"hitokoto\":\"" + string([]byte{0xff}) + "\",\"type\":\"x\"}]",
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseFormat(context.Background(), strings.NewReader(raw), FormatHitokoto); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestParseFormatNativeRemainsStrict(t *testing.T) {
	raw := `{"categories":[{"code":"x","name":"x"}],"sentences":[{"uuid":"75a45fd4-4f2f-45eb-80cb-6f0a7bcdfaf2","category":"x","content":"x"}]}`
	if _, err := ParseFormat(context.Background(), strings.NewReader(raw), FormatNative); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseFormat(context.Background(), strings.NewReader(`{"sentences":[],"sentences":[]}`), FormatNative); err == nil {
		t.Fatal("expected native strict duplicate-field error")
	}
	if _, err := ParseFormat(context.Background(), strings.NewReader(raw), "other"); err == nil {
		t.Fatal("expected invalid format error")
	}
}
