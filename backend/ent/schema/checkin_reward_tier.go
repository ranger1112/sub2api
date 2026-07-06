package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// CheckInRewardTier holds the schema definition for the CheckInRewardTier entity.
//
// 每日签到奖励分层（tier）：按充值额或综合评分匹配，命中后覆盖全局奖励区间参数。
// 管理端可增删改查；签到发奖时读取 enabled 的 tier 参与匹配。
// max_reward 会被全局 checkin_max_reward 二次夹紧（tier 永远不能突破全局上限）。
//
// 删除策略：硬删除。
type CheckInRewardTier struct {
	ent.Schema
}

func (CheckInRewardTier) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "checkin_reward_tiers"},
	}
}

func (CheckInRewardTier) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").
			MaxLen(64).
			NotEmpty().
			Comment("分层名称"),
		field.Bool("enabled").
			Default(true).
			Comment("是否启用"),
		field.String("match_type").
			MaxLen(16).
			Default("recharge").
			Comment("匹配维度: recharge | score"),
		field.Float("match_threshold").
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,8)"}).
			Default(0).
			Comment("匹配阈值（对应维度 >= 该值时命中）"),
		field.Float("min_reward").
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,8)"}).
			Default(0).
			Comment("该分层单次最小奖励"),
		field.Float("max_reward").
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,8)"}).
			Default(0).
			Comment("该分层单次最大奖励（会被全局上限二次夹紧）"),
		field.Float("base_cap").
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,8)"}).
			Default(0).
			Comment("该分层奖励区间基础上限"),
		field.Float("beta_min").
			SchemaType(map[string]string{dialect.Postgres: "decimal(10,6)"}).
			Default(1).
			Comment("幂律分布 beta 下界"),
		field.Float("beta_max").
			SchemaType(map[string]string{dialect.Postgres: "decimal(10,6)"}).
			Default(3).
			Comment("幂律分布 beta 上界"),
		field.Int("sort_order").
			Default(0).
			Comment("排序序号（越大越优先，用于同阈值 tie-break）"),
		field.Time("created_at").
			Immutable().
			Default(time.Now).
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("updated_at").
			Default(time.Now).
			UpdateDefault(time.Now).
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

func (CheckInRewardTier) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("enabled"),
		index.Fields("sort_order"),
	}
}
