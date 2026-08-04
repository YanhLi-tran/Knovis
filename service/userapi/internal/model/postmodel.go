package model

import (
	"context"
	"fmt"

	"github.com/zeromicro/go-zero/core/stores/sqlx"
)

var _ PostModel = (*customPostModel)(nil)

type (
	// PostModel is an interface to be customized, add more methods here,
	// and implement the added methods in customPostModel.
	PostModel interface {
		postModel
		// FindPage 分页查询动态列表(广场), 按创建时间倒序
		FindPage(ctx context.Context, offset, limit int64) ([]Post, int64, error)
		// FindPageByUserID 分页查询指定用户的动态, 按创建时间倒序
		FindPageByUserID(ctx context.Context, userID uint64, offset, limit int64) ([]Post, int64, error)
		// FindByUserID 查询指定用户的全部动态(注销级联删除用)
		FindByUserID(ctx context.Context, userID uint64) ([]Post, error)
		// DeleteByUserID 删除指定用户的全部动态(注销级联删除用)
		DeleteByUserID(ctx context.Context, userID uint64) error
		// IncViews 浏览数 +1
		IncViews(ctx context.Context, id uint64) error
		// UpdateSettings 更新动态设置(是否公开点赞/收藏列表)
		UpdateSettings(ctx context.Context, id uint64, showLikes, showFavorites int64) error
		withSession(session sqlx.Session) PostModel
	}

	customPostModel struct {
		*defaultPostModel
	}
)

// NewPostModel returns a model for the database table.
func NewPostModel(conn sqlx.SqlConn) PostModel {
	return &customPostModel{
		defaultPostModel: newPostModel(conn),
	}
}

func (m *customPostModel) FindPage(ctx context.Context, offset, limit int64) ([]Post, int64, error) {
	var total int64
	countQuery := fmt.Sprintf("select count(*) from %s", m.table)
	if err := m.conn.QueryRowCtx(ctx, &total, countQuery); err != nil {
		return nil, 0, err
	}

	query := fmt.Sprintf("select %s from %s order by `created_at` desc, `id` desc limit ?, ?", postRows, m.table)
	var list []Post
	if err := m.conn.QueryRowsCtx(ctx, &list, query, offset, limit); err != nil {
		return nil, 0, err
	}
	return list, total, nil
}

func (m *customPostModel) FindPageByUserID(ctx context.Context, userID uint64, offset, limit int64) ([]Post, int64, error) {
	var total int64
	countQuery := fmt.Sprintf("select count(*) from %s where `user_id` = ?", m.table)
	if err := m.conn.QueryRowCtx(ctx, &total, countQuery, userID); err != nil {
		return nil, 0, err
	}

	query := fmt.Sprintf("select %s from %s where `user_id` = ? order by `created_at` desc, `id` desc limit ?, ?", postRows, m.table)
	var list []Post
	if err := m.conn.QueryRowsCtx(ctx, &list, query, userID, offset, limit); err != nil {
		return nil, 0, err
	}
	return list, total, nil
}

func (m *customPostModel) FindByUserID(ctx context.Context, userID uint64) ([]Post, error) {
	query := fmt.Sprintf("select %s from %s where `user_id` = ?", postRows, m.table)
	var list []Post
	if err := m.conn.QueryRowsCtx(ctx, &list, query, userID); err != nil {
		return nil, err
	}
	return list, nil
}

func (m *customPostModel) DeleteByUserID(ctx context.Context, userID uint64) error {
	query := fmt.Sprintf("delete from %s where `user_id` = ?", m.table)
	_, err := m.conn.ExecCtx(ctx, query, userID)
	return err
}

func (m *customPostModel) IncViews(ctx context.Context, id uint64) error {
	query := fmt.Sprintf("update %s set `views` = `views` + 1 where `id` = ?", m.table)
	_, err := m.conn.ExecCtx(ctx, query, id)
	return err
}

func (m *customPostModel) UpdateSettings(ctx context.Context, id uint64, showLikes, showFavorites int64) error {
	query := fmt.Sprintf("update %s set `show_likes` = ?, `show_favorites` = ? where `id` = ?", m.table)
	_, err := m.conn.ExecCtx(ctx, query, showLikes, showFavorites, id)
	return err
}

func (m *customPostModel) withSession(session sqlx.Session) PostModel {
	return NewPostModel(sqlx.NewSqlConnFromSession(session))
}
