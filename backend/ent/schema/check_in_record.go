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

// CheckInRecord holds the schema definition for the CheckInRecord entity.
//
// 每日签到送余额记录：每个用户每天最多签到一次（由 (user_id, check_in_date) 唯一索引保证）。
// 赠送的奖励只增加 users.balance，不计入 users.total_recharged。
//
// 删除策略：硬删除随用户级联（FK ON DELETE CASCADE 由迁移文件维护）。
type CheckInRecord struct {
	ent.Schema
}

func (CheckInRecord) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "check_in_records"},
	}
}

func (CheckInRecord) Fields() []ent.Field {
	return []ent.Field{
		// user_id 的外键约束由迁移文件维护（REFERENCES users(id) ON DELETE CASCADE），
		// 此处不声明 ent edge，保持 schema 精简。
		field.Int64("user_id"),
		// 签到日期（应用时区），存储为 Postgres DATE，应用层传入 'YYYY-MM-DD'。
		field.Time("check_in_date").
			SchemaType(map[string]string{dialect.Postgres: "date"}),
		field.Float("reward_amount").
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,8)"}).
			Default(0),
		field.Int("streak_count").
			Default(0),
		// score 为计算出的综合评分（审计用）。
		field.Float("score").
			SchemaType(map[string]string{dialect.Postgres: "decimal(10,6)"}).
			Default(0),
		// recharge_snapshot / usage_snapshot 为计算奖励时的审计快照。
		field.Float("recharge_snapshot").
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,8)"}).
			Default(0),
		field.Float("usage_snapshot").
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,8)"}).
			Default(0),
		field.Time("created_at").
			Immutable().
			Default(time.Now).
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

func (CheckInRecord) Indexes() []ent.Index {
	return []ent.Index{
		// 每个用户每天最多一条记录（并发签到的乐观锁依据）。
		index.Fields("user_id", "check_in_date").
			Unique(),
		index.Fields("user_id", "created_at"),
		index.Fields("check_in_date"),
	}
}
