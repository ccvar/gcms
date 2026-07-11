package i18n

import "testing"

// ActiveWith：平台实例统计别站语种时，必须认那个站自己的自定义语种。
func TestActiveWithCountsForeignCustomLocales(t *testing.T) {
	m := New()
	custom := `[{"code":"tlh","name":"Klingon","tag":"tlh","og":"tlh_TLH"}]`

	// 内置 + 别站自定义：都要算（Active 会漏掉 tlh —— 这正是站点卡片语种数偏少的根因）
	got := m.ActiveWith("zh,tlh", custom)
	if len(got) != 2 || got[0].Code != "zh" || got[1].Code != "tlh" {
		t.Fatalf("ActiveWith(zh,tlh) = %+v, want [zh tlh]", got)
	}
	if n := len(m.Active("zh,tlh")); n != 1 {
		t.Fatalf("Active 不该认别站自定义语种（回归基准），got %d", n)
	}

	// 自定义语种排第一：首个（默认语种）也要对
	got = m.ActiveWith("tlh,en", custom)
	if len(got) != 2 || got[0].Code != "tlh" {
		t.Fatalf("custom-first = %+v, want tlh first", got)
	}

	// 未知码仍然滤掉；全空回退 zh
	if got = m.ActiveWith("zz", ""); len(got) != 1 || got[0].Code != "zh" {
		t.Fatalf("fallback = %+v, want [zh]", got)
	}
}
