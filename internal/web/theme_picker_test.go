package web

import (
	"strings"
	"testing"
)

func pickerCardsFromRegistry(t *testing.T) []ThemeCard {
	t.Helper()
	cards := make([]ThemeCard, 0, len(Themes))
	for _, th := range Themes {
		cards = append(cards, ThemeCard{
			ID: th.ID, Name: th.Name, Desc: th.Desc, Category: th.Category,
			Accent: themeAccentDefault[th.ID], Radius: themeRadiusDefault[th.ID], Bg: themeBg(th.ID),
		})
	}
	return cards
}

// themeByID 从注册表按 id 取主题条目（找不到时 fail）。
func themeByID(t *testing.T, id string) ThemeOption {
	t.Helper()
	for _, th := range Themes {
		if th.ID == id {
			return th
		}
	}
	t.Fatalf("theme %q not in registry", id)
	return ThemeOption{}
}

func TestWeb3GuideFamiliesHavePureWhiteSkin(t *testing.T) {
	for _, id := range []string{"briefing-desk-white", "decision-wall-white", "route-atlas-white"} {
		if got := themeBg(id); got != "#ffffff" {
			t.Errorf("themeBg(%q) = %q, want pure white", id, got)
		}
		if family := familyForTheme(id); family == id || family == "" {
			t.Errorf("familyForTheme(%q) = %q, want grouped skeleton family", id, family)
		}
	}
}

// 配色族聚合：卡数=族数、皮肤全覆盖不重不漏、骨架族用骨架对名、
// 独立皮族恢复皮肤自己的名字与描述、选中回显与激活皮规则不变。
func TestThemeFamilyCards(t *testing.T) {
	cards := pickerCardsFromRegistry(t)

	families := map[string]bool{}
	for _, th := range Themes {
		families[familyForTheme(th.ID)] = true
	}

	got := themeFamilyCards(cards, "tradewind", "zh")
	if len(got) != len(families) {
		t.Fatalf("family cards = %d, want %d（=族数）", len(got), len(families))
	}

	seen := map[string]bool{}
	selectedCount := 0
	for _, fc := range got {
		if info, isSkeleton := themeSkeletons[fc.Family]; isSkeleton {
			// 骨架族：中英对名 + 定位描述 + 英文版描述齐备。
			if fc.Name != info.Name || fc.Desc != info.Desc {
				t.Errorf("骨架族 %q 名称/描述未取自 themeSkeletons：%q / %q", fc.Family, fc.Name, fc.Desc)
			}
			if !strings.Contains(info.Name, " · ") {
				t.Errorf("骨架 %q name %q 不是「中 · 英」对名", fc.Family, info.Name)
			}
			if info.Desc == "" || themeSkeletonDescEN[fc.Family] == "" {
				t.Errorf("骨架 %q 缺中文或英文描述", fc.Family)
			}
			if len(fc.Skins) < 2 {
				t.Errorf("骨架族 %q 只有 %d 个皮肤，单皮不该按骨架成族", fc.Family, len(fc.Skins))
			}
		} else {
			// 独立皮族（族 id=皮肤 id）：来自 topbar 元老,或经 themeFamilies 显式自立
			// （设计身份超出配色变体的非 topbar 皮,如 dawnfair）。门面=皮肤自己的名字与描述。
			th := themeByID(t, fc.Family)
			if layoutForTheme(fc.Family) != "topbar" && themeFamilies[fc.Family] != fc.Family {
				t.Errorf("非 topbar 皮 %q 不该独立成族（骨架 %q,且未显式自立）", fc.Family, layoutForTheme(fc.Family))
			}
			if fc.Name != th.Name || fc.Desc != th.Desc {
				t.Errorf("独立皮族 %q 名称/描述未用皮肤自己的：%q / %q", fc.Family, fc.Name, fc.Desc)
			}
			if fc.Skins[0].ID != fc.Family {
				t.Errorf("族 %q 的带头皮肤 = %q，want 族 id 本尊", fc.Family, fc.Skins[0].ID)
			}
		}
		if len(fc.Skins) == 0 {
			t.Fatalf("family %q has no skins", fc.Family)
		}
		for _, skin := range fc.Skins {
			if seen[skin.ID] {
				t.Errorf("skin %q 重复出现", skin.ID)
			}
			seen[skin.ID] = true
			if familyForTheme(skin.ID) != fc.Family {
				t.Errorf("skin %q 分错族 %q", skin.ID, fc.Family)
			}
			if skin.Bg == "" {
				t.Errorf("skin %q 缺底色", skin.ID)
			}
			if !strings.Contains(" "+fc.Categories+" ", " "+skin.Category+" ") {
				t.Errorf("族 %q 的分类集合 %q 漏了皮肤 %q 的 %q", fc.Family, fc.Categories, skin.ID, skin.Category)
			}
		}
		if fc.Selected {
			selectedCount++
			if fc.Family != "factory-catalog" || fc.Active.ID != "tradewind" {
				t.Errorf("选中态落点 = %q/%q, want factory-catalog/tradewind", fc.Family, fc.Active.ID)
			}
		} else if fc.Active.ID != fc.Skins[0].ID {
			t.Errorf("族 %q 未选中时激活皮应为第一个 %q, got %q", fc.Family, fc.Skins[0].ID, fc.Active.ID)
		}
	}
	if selectedCount != 1 {
		t.Fatalf("selected card count = %d, want 1", selectedCount)
	}
	if len(seen) != len(Themes) {
		t.Fatalf("覆盖皮肤 %d 个, want %d", len(seen), len(Themes))
	}

	// editorial 族：默认设计 + 审定并入的两个纯配色变体，注册表顺序。
	var ed *ThemeFamilyCard
	for i := range got {
		if got[i].Family == "editorial" {
			ed = &got[i]
			break
		}
	}
	if ed == nil {
		t.Fatalf("missing editorial family card")
	}
	wantEd := []string{"editorial", "paperwhite", "citrus"}
	if len(ed.Skins) != len(wantEd) {
		t.Fatalf("editorial family skins = %d, want %d", len(ed.Skins), len(wantEd))
	}
	for i, id := range wantEd {
		if ed.Skins[i].ID != id {
			t.Errorf("editorial family skin[%d] = %q, want %q", i, ed.Skins[i].ID, id)
		}
	}
	if ed.Name != "编辑 · Editorial" {
		t.Errorf("editorial family name = %q, want 编辑 · Editorial", ed.Name)
	}

	// 元老独立皮：topbar 皮除并入 editorial 族者外全部独立成卡（单皮）。
	singles := 0
	for _, th := range Themes {
		if layoutForTheme(th.ID) != "topbar" {
			continue
		}
		if _, merged := themeFamilies[th.ID]; merged || th.ID == "editorial" {
			continue
		}
		singles++
		found := false
		for _, fc := range got {
			if fc.Family == th.ID {
				found = true
				if len(fc.Skins) != 1 || fc.Skins[0].ID != th.ID {
					t.Errorf("元老皮 %q 应独立单皮成卡, got %d skins", th.ID, len(fc.Skins))
				}
			}
		}
		if !found {
			t.Errorf("元老皮 %q 没有自己的卡", th.ID)
		}
	}
	if singles == 0 {
		t.Fatalf("no independent topbar veteran cards found")
	}

	// 工厂九骨架族：各 5 皮（4 原生 + 1 净白系）、分类纯 factory。
	for _, family := range []string{"factory-catalog", "factory-showcase", "factory-onepage", "factory-solutions", "factory-engineering", "factory-trade", "factory-sidebar", "factory-vision", "factory-herofold"} {
		var found *ThemeFamilyCard
		for i := range got {
			if got[i].Family == family {
				found = &got[i]
				break
			}
		}
		if found == nil {
			t.Fatalf("missing %s card", family)
		}
		if len(found.Skins) != 5 || found.Categories != "factory" {
			t.Errorf("%s: skins=%d categories=%q, want 5/factory", family, len(found.Skins), found.Categories)
		}
	}

	// 英文后台：骨架族名只留英文半段、描述用英文版；独立皮族直接用传入卡片的名字
	// （admin.go 传入的 cards 已经 themeOptionForAdmin 本地化，聚合函数不再自行转换）。
	en := themeFamilyCards(cards, "editorial", "en")
	for _, fc := range en {
		if _, isSkeleton := themeSkeletons[fc.Family]; isSkeleton {
			if strings.Contains(fc.Name, " · ") {
				t.Errorf("EN skeleton family name %q 未转英文", fc.Name)
			}
			if fc.Desc != themeSkeletonDescEN[fc.Family] {
				t.Errorf("EN skeleton family %q desc 未用英文版", fc.Family)
			}
		} else {
			th := themeByID(t, fc.Family)
			if fc.Name != th.Name {
				t.Errorf("EN 独立皮族 %q 不该改写传入卡片名：%q", fc.Family, fc.Name)
			}
		}
	}

	// 注册表外的残留选中值：追加「自定义」兜底卡保住选中态。
	stale := themeFamilyCards(cards, "no-such-theme", "zh")
	if len(stale) != len(families)+1 {
		t.Fatalf("stale-selected cards = %d, want %d", len(stale), len(families)+1)
	}
	last := stale[len(stale)-1]
	if last.Family != "custom" || !last.Selected || last.Active.ID != "no-such-theme" || len(last.Skins) != 1 {
		t.Fatalf("兜底卡 = %+v, want custom/selected/no-such-theme", last)
	}
	for _, fc := range stale[:len(stale)-1] {
		if fc.Selected {
			t.Errorf("stale selected 不应点亮注册表族卡 %q", fc.Family)
		}
	}
}

// themeFamilies 显式登记表卫生：键值都是注册表皮肤、目标族先于成员出现（门面=带头皮肤）、
// 只允许同骨架（topbar）内合并、且族 id 不得与骨架名相撞（命名优先级是骨架表优先）。
func TestThemeFamiliesRegistry(t *testing.T) {
	pos := map[string]int{}
	for i, th := range Themes {
		pos[th.ID] = i
	}
	for skin, family := range themeFamilies {
		si, ok := pos[skin]
		if !ok {
			t.Errorf("themeFamilies 含未注册皮肤 %q", skin)
			continue
		}
		fi, ok := pos[family]
		if !ok {
			t.Errorf("themeFamilies[%q] 指向未注册族门面 %q", skin, family)
			continue
		}
		if fi >= si && family != skin {
			// 自立族(门面=成员自身,如 dawnfair)豁免先后序要求。
			t.Errorf("族门面 %q 必须在成员 %q 之前注册（卡片名字取带头皮肤）", family, skin)
		}
		if layoutForTheme(skin) != layoutForTheme(family) {
			t.Errorf("跨骨架合并：%q(%s) → %q(%s)", skin, layoutForTheme(skin), family, layoutForTheme(family))
		}
		if _, clash := themeSkeletons[family]; clash {
			t.Errorf("族 id %q 与骨架名相撞（骨架表命名会盖过皮肤名）", family)
		}
	}
	// topbar 元老皮的族 id=自身，同样不得与骨架名相撞。
	for _, th := range Themes {
		if layoutForTheme(th.ID) != "topbar" {
			continue
		}
		if _, clash := themeSkeletons[th.ID]; clash {
			t.Errorf("topbar 皮 %q 与骨架名相撞", th.ID)
		}
	}
}

// 底色登记表不含注册表外的皮肤（防拼错 id 的死键）。
func TestThemeBgKeysRegistered(t *testing.T) {
	ids := map[string]bool{}
	for _, th := range Themes {
		ids[th.ID] = true
	}
	for id := range themeBgDefault {
		if !ids[id] {
			t.Errorf("themeBgDefault 含未注册皮肤 %q", id)
		}
	}
}
