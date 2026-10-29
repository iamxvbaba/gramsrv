package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
)

// PeerMainTabStore keeps the profile tab a peer opens first.
type PeerMainTabStore struct {
	db sqlcgen.DBTX
}

func NewPeerMainTabStore(db sqlcgen.DBTX) *PeerMainTabStore { return &PeerMainTabStore{db: db} }

func (s *PeerMainTabStore) SetMainTab(ctx context.Context, peer domain.Peer, tab string) error {
	if tab == "" {
		_, err := s.db.Exec(ctx, `DELETE FROM peer_main_tabs WHERE peer_type = $1 AND peer_id = $2`, string(peer.Type), peer.ID)
		return err
	}
	_, err := s.db.Exec(ctx, `INSERT INTO peer_main_tabs (peer_type, peer_id, tab) VALUES ($1, $2, $3)
ON CONFLICT (peer_type, peer_id) DO UPDATE SET tab = EXCLUDED.tab, updated_at = now()`, string(peer.Type), peer.ID, tab)
	if err != nil {
		return fmt.Errorf("set main tab: %w", err)
	}
	return nil
}

func (s *PeerMainTabStore) MainTab(ctx context.Context, peer domain.Peer) (string, error) {
	var tab string
	err := s.db.QueryRow(ctx, `SELECT tab FROM peer_main_tabs WHERE peer_type = $1 AND peer_id = $2`, string(peer.Type), peer.ID).Scan(&tab)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("main tab: %w", err)
	}
	return tab, nil
}
