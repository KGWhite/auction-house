// Package item owns item templates and the generation of auction rounds.
package item

import (
	"context"
	"fmt"

	"auctionhouse/internal/db"
)

// Item types.
var Types = []string{"weapon", "shield", "armor", "helmet", "gloves", "boots", "ring", "amulet"}

// Template is the seed definition of an item template.
type Template struct {
	Name      string
	Type      string
	BaseValue int64
	MinLevel  int
}

// tierMinLevels are the min_level of the four quality tiers of every type.
var tierMinLevels = [4]int{1, 15, 30, 50}

type typeDef struct {
	typ    string
	names  [4]string
	values [4]int64
}

var typeDefs = []typeDef{
	{"weapon", [4]string{"鏽蝕短劍", "精鋼長劍", "騎士戰刃", "屠龍巨劍"}, [4]int64{100, 260, 650, 1500}},
	{"shield", [4]string{"木製圓盾", "鐵製塔盾", "守衛之盾", "聖光壁壘"}, [4]int64{80, 220, 560, 1300}},
	{"armor", [4]string{"粗皮甲", "鎖子甲", "板甲", "龍鱗鎧"}, [4]int64{90, 240, 600, 1400}},
	{"helmet", [4]string{"皮帽", "鐵盔", "騎士頭盔", "王者皇冠"}, [4]int64{60, 160, 420, 1000}},
	{"gloves", [4]string{"布手套", "皮手套", "鋼鐵護手", "巨人之握"}, [4]int64{50, 140, 380, 900}},
	{"boots", [4]string{"草鞋", "皮靴", "鐵靴", "疾風戰靴"}, [4]int64{50, 140, 380, 900}},
	{"ring", [4]string{"銅戒指", "銀戒指", "黃金戒指", "蒼穹之戒"}, [4]int64{70, 200, 520, 1250}},
	{"amulet", [4]string{"骨製護符", "銀製護符", "秘法護符", "永恆聖物"}, [4]int64{75, 210, 540, 1300}},
}

// DefaultTemplates returns the 32 seed templates: every type in four tiers
// whose min_level are 1, 15, 30 and 50.
func DefaultTemplates() []Template {
	out := make([]Template, 0, len(typeDefs)*len(tierMinLevels))
	for _, d := range typeDefs {
		for tier := range tierMinLevels {
			out = append(out, Template{Name: d.names[tier], Type: d.typ, BaseValue: d.values[tier], MinLevel: tierMinLevels[tier]})
		}
	}
	return out
}

// SeedTemplates inserts DefaultTemplates when the table is empty and returns
// how many rows it inserted.
func SeedTemplates(ctx context.Context, q *db.Queries) (int, error) {
	n, err := q.CountItemTemplates(ctx)
	if err != nil {
		return 0, fmt.Errorf("count templates: %w", err)
	}
	if n > 0 {
		return 0, nil
	}
	tmpls := DefaultTemplates()
	for _, t := range tmpls {
		if _, err := q.CreateItemTemplate(ctx, db.CreateItemTemplateParams{
			Name: t.Name, Type: t.Type, BaseValue: t.BaseValue, MinLevel: int32(t.MinLevel),
		}); err != nil {
			return 0, fmt.Errorf("insert template %q: %w", t.Name, err)
		}
	}
	return len(tmpls), nil
}
