package alert

import "testing"

// TestTemplateKeyCompleteness:每個 key 在三語都要有,避免漏譯靜默 fallback 到 en。
func TestTemplateKeyCompleteness(t *testing.T) {
	langs := []string{"en", "zh-TW", "zh-CN"}
	for key := range templates["en"] {
		for _, lang := range langs {
			if _, ok := templates[lang][key]; !ok {
				t.Errorf("模板 key %q 缺 %s", key, lang)
			}
		}
	}
}
