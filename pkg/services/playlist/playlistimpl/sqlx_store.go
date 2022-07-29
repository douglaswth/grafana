package playlistimpl

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/grafana/grafana/pkg/models"
	"github.com/grafana/grafana/pkg/services/playlist"
	"github.com/grafana/grafana/pkg/services/sqlstore/migrator"
	"github.com/jmoiron/sqlx"
)

type sqlxStore struct {
	sqlxdb  *sqlx.DB
	dialect migrator.Dialect
}

func (s *sqlxStore) insertWithReturningId(ctx context.Context, tx *sqlx.Tx, p *playlist.Playlist) error {
	if s.dialect.DriverName() == "postgres" {
		query := fmt.Sprintf("INSERT INTO playlist (name, %s, org_id, uid) VALUES (?, ?, ?, ?) RETURNING id", s.dialect.Quote("interval"))
		var id int64
		err := tx.GetContext(ctx, &id, s.sqlxdb.Rebind(query), p.Name, p.Interval, p.OrgId, p.UID)
		if err != nil {
			return err
		}
		p.Id = id
	} else {
		query := fmt.Sprintf("INSERT INTO playlist (name, %s, org_id, uid) VALUES (:name, :interval, :org_id, :uid)", s.dialect.Quote("interval"))
		res, err := tx.NamedExecContext(ctx, s.sqlxdb.Rebind(query), &p)
		if err != nil {
			return err
		}
		p.Id, err = res.LastInsertId()
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *sqlxStore) Insert(ctx context.Context, cmd *playlist.CreatePlaylistCommand) (*playlist.Playlist, error) {
	p := playlist.Playlist{}
	var err error
	uid, err := newGenerateAndValidateNewPlaylistUid(ctx, s.sqlxdb, cmd.OrgId)
	if err != nil {
		return nil, err
	}

	p = playlist.Playlist{
		Name:     cmd.Name,
		Interval: cmd.Interval,
		OrgId:    cmd.OrgId,
		UID:      uid,
	}

	tx, err := s.sqlxdb.Beginx()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	err = s.insertWithReturningId(ctx, tx, &p)
	if err != nil {
		return nil, err
	}

	if len(cmd.Items) > 0 {
		playlistItems := make([]playlist.PlaylistItem, 0)
		for _, item := range cmd.Items {
			playlistItems = append(playlistItems, playlist.PlaylistItem{
				PlaylistId: p.Id,
				Type:       item.Type,
				Value:      item.Value,
				Order:      item.Order,
				Title:      item.Title,
			})
		}
		query := fmt.Sprintf("INSERT INTO playlist_item (playlist_id, type, value, title, %s) VALUES (:playlist_id, :type, :value, :title, :order)", s.dialect.Quote("order"))
		_, err = tx.NamedExecContext(ctx, query, playlistItems)
		if err != nil {
			return nil, err
		}
	}

	if err = tx.Commit(); err != nil {
		return nil, err
	}

	return &p, err
}

func (s *sqlxStore) Update(ctx context.Context, cmd *playlist.UpdatePlaylistCommand) (*playlist.PlaylistDTO, error) {
	dto := playlist.PlaylistDTO{}

	// Get the id of playlist to be updated with orgId and UID
	existingPlaylist, err := s.Get(ctx, &playlist.GetPlaylistByUidQuery{UID: cmd.UID, OrgId: cmd.OrgId})
	if err != nil {
		return nil, err
	}

	// Create object to be update to
	p := playlist.Playlist{
		Id:       existingPlaylist.Id,
		UID:      cmd.UID,
		OrgId:    cmd.OrgId,
		Name:     cmd.Name,
		Interval: cmd.Interval,
	}

	tx, err := s.sqlxdb.Beginx()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	query := fmt.Sprintf("UPDATE playlist SET uid=:uid, org_id=:org_id, name=:name, %s=:interval WHERE id=:id", s.dialect.Quote("interval"))
	_, err = tx.NamedExecContext(ctx, query, p)
	if err != nil {
		return nil, err
	}

	if _, err = tx.ExecContext(ctx, s.sqlxdb.Rebind("DELETE FROM playlist_item WHERE playlist_id = ?"), p.Id); err != nil {
		return nil, err
	}

	playlistItems := make([]playlist.PlaylistItem, 0)

	for index, item := range cmd.Items {
		playlistItems = append(playlistItems, playlist.PlaylistItem{
			PlaylistId: p.Id,
			Type:       item.Type,
			Value:      item.Value,
			Order:      index,
			Title:      item.Title,
		})
	}
	query = fmt.Sprintf("INSERT INTO playlist_item (playlist_id, type, value, title, %s) VALUES (:playlist_id, :type, :value, :title, :order)", s.dialect.Quote("order"))
	_, err = tx.NamedExecContext(ctx, query, playlistItems)
	if err != nil {
		return nil, err
	}

	if err = tx.Commit(); err != nil {
		return nil, err
	}

	return &dto, err
}

func (s *sqlxStore) Get(ctx context.Context, query *playlist.GetPlaylistByUidQuery) (*playlist.Playlist, error) {
	if query.UID == "" || query.OrgId == 0 {
		return nil, playlist.ErrCommandValidationFailed
	}

	p := playlist.Playlist{}
	err := s.sqlxdb.GetContext(ctx, &p, s.sqlxdb.Rebind("SELECT * FROM playlist WHERE uid=? AND org_id=?"), query.UID, query.OrgId)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, playlist.ErrPlaylistNotFound
		}
		return nil, err
	}
	return &p, err
}

func (s *sqlxStore) Delete(ctx context.Context, cmd *playlist.DeletePlaylistCommand) error {
	if cmd.UID == "" || cmd.OrgId == 0 {
		return playlist.ErrCommandValidationFailed
	}
	tx, err := s.sqlxdb.Beginx()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	p := playlist.Playlist{}
	if err = s.sqlxdb.GetContext(ctx, &p, s.sqlxdb.Rebind("SELECT * FROM playlist WHERE uid=? AND org_id=?"), cmd.UID, cmd.OrgId); err != nil {
		if err == sql.ErrNoRows {
			return nil
		}
		return err
	}

	if _, err = tx.ExecContext(ctx, s.sqlxdb.Rebind("DELETE FROM playlist WHERE uid = ? and org_id = ?"), cmd.UID, cmd.OrgId); err != nil {
		return err
	}

	if _, err = tx.ExecContext(ctx, s.sqlxdb.Rebind("DELETE FROM playlist_item WHERE playlist_id = ?"), p.Id); err != nil {
		return err
	}

	if err = tx.Commit(); err != nil {
		return err
	}
	return err
}

func (s *sqlxStore) List(ctx context.Context, query *playlist.GetPlaylistsQuery) (playlist.Playlists, error) {
	playlists := make(playlist.Playlists, 0)
	if query.OrgId == 0 {
		return playlists, playlist.ErrCommandValidationFailed
	}

	var err error
	if query.Name == "" {
		err = s.sqlxdb.SelectContext(
			ctx, &playlists, s.sqlxdb.Rebind("SELECT * FROM playlist WHERE org_id = ? LIMIT ?"), query.OrgId, query.Limit)
	} else {
		err = s.sqlxdb.SelectContext(
			ctx, &playlists, s.sqlxdb.Rebind("SELECT * FROM playlist WHERE org_id = ? AND name LIKE ? LIMIT ?"), query.OrgId, "%"+query.Name+"%", query.Limit)
	}
	return playlists, err
}

func (s *sqlxStore) GetItems(ctx context.Context, query *playlist.GetPlaylistItemsByUidQuery) ([]playlist.PlaylistItem, error) {
	var playlistItems = make([]playlist.PlaylistItem, 0)
	if query.PlaylistUID == "" || query.OrgId == 0 {
		return playlistItems, models.ErrCommandValidationFailed
	}

	var p = playlist.Playlist{}
	err := s.sqlxdb.GetContext(ctx, &p, s.sqlxdb.Rebind("SELECT * FROM playlist WHERE uid=? AND org_id=?"), query.PlaylistUID, query.OrgId)
	if err != nil {
		return playlistItems, err
	}

	err = s.sqlxdb.SelectContext(ctx, &playlistItems, s.sqlxdb.Rebind("SELECT * FROM playlist_item WHERE playlist_id=?"), p.Id)
	return playlistItems, err
}

func newGenerateAndValidateNewPlaylistUid(ctx context.Context, db *sqlx.DB, orgId int64) (string, error) {
	for i := 0; i < 3; i++ {
		uid := generateNewUid()
		p := playlist.Playlist{}
		err := db.GetContext(ctx, &p, db.Rebind("SELECT * FROM playlist WHERE uid=? AND org_id=?"), uid, orgId)
		if err != nil {
			if err == sql.ErrNoRows {
				return uid, nil
			}
			return "", err
		}
	}

	return "", models.ErrPlaylistFailedGenerateUniqueUid
}
