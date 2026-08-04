package model

import (
	"context"
	"fmt"
	"strings"

	"github.com/zeromicro/go-zero/core/stores/sqlx"
)

var _ UserModel = (*customUserModel)(nil)

type (
	// UserModel is an interface to be customized, add more methods here,
	// and implement the added methods in customUserModel.
	UserModel interface {
		userModel
		// FindPage 分页查询用户列表, 按 id 倒序
		FindPage(ctx context.Context, offset, limit int64) ([]User, int64, error)
		// UpdateFields 按字段集合更新用户(仅本表列名, 无注入风险)
		UpdateFields(ctx context.Context, id uint64, fields map[string]interface{}) error
		withSession(session sqlx.Session) UserModel
	}

	customUserModel struct {
		*defaultUserModel
	}
)

// NewUserModel returns a model for the database table.
func NewUserModel(conn sqlx.SqlConn) UserModel {
	return &customUserModel{
		defaultUserModel: newUserModel(conn),
	}
}

func (m *customUserModel) FindPage(ctx context.Context, offset, limit int64) ([]User, int64, error) {
	var total int64
	countQuery := fmt.Sprintf("select count(*) from %s", m.table)
	if err := m.conn.QueryRowCtx(ctx, &total, countQuery); err != nil {
		return nil, 0, err
	}

	query := fmt.Sprintf("select %s from %s order by `id` desc limit ?, ?", userRows, m.table)
	var list []User
	if err := m.conn.QueryRowsCtx(ctx, &list, query, offset, limit); err != nil {
		return nil, 0, err
	}
	return list, total, nil
}

func (m *customUserModel) UpdateFields(ctx context.Context, id uint64, fields map[string]interface{}) error {
	if len(fields) == 0 {
		return nil
	}
	sets := make([]string, 0, len(fields))
	args := make([]interface{}, 0, len(fields)+1)
	for k, v := range fields {
		sets = append(sets, "`"+k+"` = ?")
		args = append(args, v)
	}
	args = append(args, id)
	query := fmt.Sprintf("update %s set %s where `id` = ?", m.table, strings.Join(sets, ", "))
	_, err := m.conn.ExecCtx(ctx, query, args...)
	return err
}

func (m *customUserModel) withSession(session sqlx.Session) UserModel {
	return NewUserModel(sqlx.NewSqlConnFromSession(session))
}
