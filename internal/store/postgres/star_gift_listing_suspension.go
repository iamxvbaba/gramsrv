package postgres

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
)

// Заморозка аккаунта и маркет подарков связаны напрямую: замороженный продавец
// не торгует. Его листинги не удаляются, а помечаются suspended — цена и время
// выставления обязаны пережить цикл заморозки, иначе разморозка уже не смогла бы
// вернуть подарок на витрину. Все чтения маркета (ListResaleStarGifts,
// UniqueStarGiftValueInfo, resell_amount уникального подарка, проекции каталога)
// отфильтровывают suspended, а guard-триггер не даёт замороженному создать
// листинг.

// syncStarGiftListingsForAccountFreeze переключает витрину замороженного
// аккаунта и пересчитывает затронутые проекции каталога. Вызывается из той же
// транзакции, что и запись заморозки, поэтому «аккаунт frozen, но листинг ещё
// продаётся» не наблюдаемо ни из одного читателя.
//
// updated_at намеренно не трогается: сортировка витрины опирается на него, и
// возвращение к продаже с прежним updated_at восстанавливает исходный порядок
// выставления. Версионный счётчик листинга, наоборот, инкрементируется — версия
// входит в id платёжной формы, поэтому покупка по форме, выданной до заморозки,
// после разморозки не проходит как реплей старой цены.
func syncStarGiftListingsForAccountFreeze(ctx context.Context, db sqlcgen.DBTX, userID int64, frozen bool) error {
	if db == nil || userID <= 0 {
		return nil
	}
	rows, err := db.Query(ctx, `UPDATE star_gift_listings l SET suspended=$2,version=l.version+1
  FROM unique_star_gifts u
 WHERE l.unique_gift_id=u.id AND l.seller_peer_type='user' AND l.seller_peer_id=$1 AND l.suspended<>$2
RETURNING u.gift_id`, userID, frozen)
	if err != nil {
		return fmt.Errorf("suspend star gift listings: %w", err)
	}
	seen := make(map[int64]struct{})
	var giftIDs []int64
	for rows.Next() {
		var giftID int64
		if err := rows.Scan(&giftID); err != nil {
			rows.Close()
			return fmt.Errorf("scan suspended star gift listing: %w", err)
		}
		if _, duplicate := seen[giftID]; duplicate {
			continue
		}
		seen[giftID] = struct{}{}
		giftIDs = append(giftIDs, giftID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("iterate suspended star gift listings: %w", err)
	}
	rows.Close()
	sort.Slice(giftIDs, func(i, j int) bool { return giftIDs[i] < giftIDs[j] })
	for _, giftID := range giftIDs {
		if err := updateStarGiftResaleProjection(ctx, db, giftID); err != nil {
			return fmt.Errorf("refresh resale projection for gift %d: %w", giftID, err)
		}
	}
	return nil
}

// rejectFrozenStarGiftSeller закрывает выставление подарка на маркет, пока
// аккаунт продавца заморожен. FOR SHARE сериализуется с записью заморозки
// (account_restrictions.user_id — его PK): пересекающаяся заморозка разрешается
// до нашего коммита, а не после. Никогда не замораживавшиеся аккаунты строки не
// имеют и проходят.
func rejectFrozenStarGiftSeller(ctx context.Context, db sqlcgen.DBTX, userID int64) error {
	var frozen bool
	err := db.QueryRow(ctx, `SELECT frozen FROM account_restrictions WHERE user_id=$1 FOR SHARE`, userID).Scan(&frozen)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("check star gift seller freeze: %w", err)
	}
	if frozen {
		return domain.ErrStarGiftResaleUnavailable
	}
	return nil
}
