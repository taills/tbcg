package runtime

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/taills/tbcg/server/internal/content/catalog"
	"github.com/taills/tbcg/server/internal/persistence"
)

var (
	ErrOwnerLost           = errors.New("runtime: realm owner epoch changed")
	ErrIdempotencyConflict = errors.New("runtime: idempotency key reused with another payload")
	ErrVersionConflict     = errors.New("runtime: state version conflict")
	ErrInsufficient        = errors.New("runtime: insufficient items or currency")
)

// Tx 是持有 owner guard 共享锁的运行面事务。
type Tx struct {
	*sql.Tx
	Ctx       context.Context
	Realm     *Realm
	Actor     string
	Operation string
	Key       string
	// Content 覆盖本事务使用的 Release（固定 Release 的实例结算时使用）；为空时用当前 Release。
	Content *catalog.Catalog
	after   []func()
	events  int
}

func (tx *Tx) content() *catalog.Catalog {
	if tx.Content != nil {
		return tx.Content
	}
	return tx.Realm.content
}

// After 注册仅在提交成功后执行的内存状态更新。
func (tx *Tx) After(fn func()) { tx.after = append(tx.after, fn) }

// Tx 运行无幂等结果的运行面事务（移动、装备等基于 state_version CAS 的操作）。
func (r *Realm) Tx(ctx context.Context, fn func(*Tx) error) error {
	tx, err := r.begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.commit()
}

func (r *Realm) begin(ctx context.Context) (*Tx, error) {
	sqlTx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin runtime transaction: %w", err)
	}
	tx := &Tx{Tx: sqlTx, Ctx: ctx, Realm: r}
	var epoch int64
	err = sqlTx.QueryRowContext(ctx, `SELECT owner_epoch FROM owner_guards WHERE game_id=$1 AND realm_id=$2 FOR SHARE`, r.GameID, r.RealmID).Scan(&epoch)
	if err != nil || epoch != r.Epoch {
		_ = sqlTx.Rollback()
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("lock realm owner guard: %w", err)
		}
		return nil, ErrOwnerLost
	}
	return tx, nil
}

func (tx *Tx) commit() error {
	if err := tx.Tx.Commit(); err != nil {
		if persistence.IsCommitOutcomeUnknown(err) {
			return errors.Join(ErrOutcomeUnknown, err)
		}
		return fmt.Errorf("commit runtime transaction: %w", err)
	}
	for _, fn := range tx.after {
		fn()
	}
	return nil
}

// Operation 以 operation_results 实现幂等：同键同载荷重放返回原结果，不重复执行。
// 返回的 replay 为 true 表示结果来自既有提交。
func (r *Realm) Operation(ctx context.Context, actor, operation, key string, payload any, fn func(*Tx) (any, error)) (json.RawMessage, bool, error) {
	if !validKey(key) {
		return nil, false, Fail("invalid_idempotency_key", "幂等键必须是 8–128 个安全字符")
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, false, err
	}
	hash := sha256.Sum256(append([]byte(operation+"\x00"), payloadBytes...))
	tx, err := r.begin(ctx)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = tx.Rollback() }()
	tx.Actor, tx.Operation, tx.Key = actor, operation, key
	result, err := tx.ExecContext(ctx, `INSERT INTO operation_results (game_id,realm_id,actor_subject,operation_name,idempotency_key,payload_hash,result)
VALUES ($1,$2,$3,$4,$5,$6,'{}'::jsonb) ON CONFLICT DO NOTHING`, r.GameID, r.RealmID, actor, operation, key, hash[:])
	if err != nil {
		return nil, false, fmt.Errorf("reserve operation: %w", err)
	}
	if rows, _ := result.RowsAffected(); rows == 0 {
		var storedHash, stored []byte
		if err := tx.QueryRowContext(ctx, `SELECT payload_hash,result FROM operation_results WHERE game_id=$1 AND realm_id=$2 AND actor_subject=$3 AND operation_name=$4 AND idempotency_key=$5`,
			r.GameID, r.RealmID, actor, operation, key).Scan(&storedHash, &stored); err != nil {
			return nil, false, fmt.Errorf("read existing operation: %w", err)
		}
		if hex.EncodeToString(storedHash) != hex.EncodeToString(hash[:]) {
			return nil, false, ErrIdempotencyConflict
		}
		_ = tx.Rollback()
		r.Resync(ctx, actor)
		return stored, true, nil
	}
	value, err := fn(tx)
	if err != nil {
		return nil, false, err
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE operation_results SET result=$6::jsonb WHERE game_id=$1 AND realm_id=$2 AND actor_subject=$3 AND operation_name=$4 AND idempotency_key=$5`,
		r.GameID, r.RealmID, actor, operation, key, encoded); err != nil {
		return nil, false, fmt.Errorf("save operation result: %w", err)
	}
	if err := tx.Emit("completed", operation+".completed", map[string]any{"result": json.RawMessage(encoded)}); err != nil {
		return nil, false, err
	}
	if err := tx.commit(); err != nil {
		if errors.Is(err, ErrOutcomeUnknown) {
			r.Resync(ctx, actor)
		}
		return nil, false, err
	}
	return encoded, false, nil
}

func validKey(key string) bool {
	if len(key) < 8 || len(key) > 128 {
		return false
	}
	for _, char := range key {
		if !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || strings.ContainsRune("._~-:", char)) {
			return false
		}
	}
	return true
}

// Emit 在当前幂等操作中写入 outbox 事件（与业务数据同事务）。
func (tx *Tx) Emit(eventKey, eventType string, payload any) error {
	if tx.Operation == "" {
		return errors.New("outbox events require an idempotent operation")
	}
	tx.events++
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(tx.Ctx, `INSERT INTO outbox (event_id,game_id,realm_id,actor_subject,operation_name,idempotency_key,event_key,event_type,payload)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9::jsonb) ON CONFLICT DO NOTHING`, NewUUID(), tx.Realm.GameID, tx.Realm.RealmID, tx.Actor, tx.Operation, tx.Key,
		fmt.Sprintf("%s#%d", eventKey, tx.events), eventType, encoded)
	if err != nil {
		return fmt.Errorf("enqueue outbox event: %w", err)
	}
	return nil
}

// NewUUID 返回随机 UUIDv4 文本。
func NewUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// ---- 背包与货币（Character/Inventory owner 边界内的共享原语） ----

// Inventory 读取角色背包。
func (tx *Tx) Inventory(characterID string) (map[string]int, error) {
	return readInventory(tx.Ctx, tx.Tx, tx.Realm, characterID)
}

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func readInventory(ctx context.Context, q queryer, r *Realm, characterID string) (map[string]int, error) {
	rows, err := q.QueryContext(ctx, `SELECT item_id, quantity FROM inventory_items WHERE game_id=$1 AND realm_id=$2 AND character_id=$3`, r.GameID, r.RealmID, characterID)
	if err != nil {
		return nil, fmt.Errorf("read inventory: %w", err)
	}
	defer rows.Close()
	items := map[string]int{}
	for rows.Next() {
		var item string
		var quantity int
		if err := rows.Scan(&item, &quantity); err != nil {
			return nil, err
		}
		items[item] = quantity
	}
	return items, rows.Err()
}

// AddItem 增加物品，受 Release 中的堆叠上限约束。
func (tx *Tx) AddItem(characterID, itemID string, quantity int) error {
	item := tx.content().Items[itemID]
	if item == nil || quantity < 1 || quantity > 9999 {
		return Fail("invalid_item", "物品不存在于当前 Release")
	}
	limit := int64(min(item.Public.StackLimit*10, 9999))
	var owned int64
	err := tx.QueryRowContext(tx.Ctx, `SELECT quantity FROM inventory_items WHERE game_id=$1 AND realm_id=$2 AND character_id=$3 AND item_id=$4 FOR UPDATE`,
		tx.Realm.GameID, tx.Realm.RealmID, characterID, itemID).Scan(&owned)
	exists := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("lock inventory item: %w", err)
	}
	// 先在行锁下计算新总数，超限返回稳定错误而不是触发数据库约束。
	if owned+int64(quantity) > limit {
		return Fail("inventory_full", "背包中该物品数量已达上限")
	}
	if exists {
		_, err = tx.ExecContext(tx.Ctx, `UPDATE inventory_items SET quantity=quantity+$5, updated_at=now() WHERE game_id=$1 AND realm_id=$2 AND character_id=$3 AND item_id=$4`,
			tx.Realm.GameID, tx.Realm.RealmID, characterID, itemID, quantity)
	} else {
		_, err = tx.ExecContext(tx.Ctx, `INSERT INTO inventory_items (game_id,realm_id,character_id,item_id,quantity) VALUES ($1,$2,$3,$4,$5)`,
			tx.Realm.GameID, tx.Realm.RealmID, characterID, itemID, quantity)
	}
	if err != nil {
		return fmt.Errorf("add inventory item: %w", err)
	}
	return nil
}

// AddItems 批量增加物品。
func (tx *Tx) AddItems(characterID string, stacks []Stack) error {
	for _, stack := range stacks {
		if err := tx.AddItem(characterID, stack.ItemID, stack.Quantity); err != nil {
			return err
		}
	}
	return nil
}

// Stack 是物品与数量。
type Stack struct {
	ItemID   string `json:"item_id"`
	Quantity int    `json:"quantity"`
}

// RemoveItem 扣除物品；数量不足时返回 ErrInsufficient 且不修改。
func (tx *Tx) RemoveItem(characterID, itemID string, quantity int) error {
	if quantity < 1 {
		return Fail("invalid_quantity", "数量无效")
	}
	var owned int
	err := tx.QueryRowContext(tx.Ctx, `SELECT quantity FROM inventory_items WHERE game_id=$1 AND realm_id=$2 AND character_id=$3 AND item_id=$4 FOR UPDATE`,
		tx.Realm.GameID, tx.Realm.RealmID, characterID, itemID).Scan(&owned)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && owned < quantity) {
		return Fail("insufficient_items", "物品数量不足")
	}
	if err != nil {
		return fmt.Errorf("lock inventory item: %w", err)
	}
	if owned == quantity {
		_, err = tx.ExecContext(tx.Ctx, `DELETE FROM inventory_items WHERE game_id=$1 AND realm_id=$2 AND character_id=$3 AND item_id=$4`,
			tx.Realm.GameID, tx.Realm.RealmID, characterID, itemID)
	} else {
		_, err = tx.ExecContext(tx.Ctx, `UPDATE inventory_items SET quantity=quantity-$5, updated_at=now() WHERE game_id=$1 AND realm_id=$2 AND character_id=$3 AND item_id=$4`,
			tx.Realm.GameID, tx.Realm.RealmID, characterID, itemID, quantity)
	}
	if err != nil {
		return fmt.Errorf("remove inventory item: %w", err)
	}
	return nil
}

// Balance 读取角色货币余额（加行锁）。
func (tx *Tx) Balance(characterID string) (int64, error) {
	var balance int64
	err := tx.QueryRowContext(tx.Ctx, `SELECT balance FROM wallets WHERE game_id=$1 AND realm_id=$2 AND character_id=$3 AND currency_code=$4 FOR UPDATE`,
		tx.Realm.GameID, tx.Realm.RealmID, characterID, CurrencyCode).Scan(&balance)
	if err != nil {
		return 0, fmt.Errorf("read wallet: %w", err)
	}
	return balance, nil
}

// AdjustCurrency 在幂等操作内改变余额并写账本；余额不可为负。
func (tx *Tx) AdjustCurrency(characterID string, delta int64, line string) (int64, error) {
	if tx.Operation == "" {
		return 0, errors.New("currency changes require an idempotent operation")
	}
	if delta == 0 {
		return tx.Balance(characterID)
	}
	var balance int64
	err := tx.QueryRowContext(tx.Ctx, `UPDATE wallets SET balance=balance+$5, updated_at=now()
WHERE game_id=$1 AND realm_id=$2 AND character_id=$3 AND currency_code=$4 AND balance+$5 >= 0 RETURNING balance`,
		tx.Realm.GameID, tx.Realm.RealmID, characterID, CurrencyCode, delta).Scan(&balance)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, Fail("insufficient_currency", "货币不足")
	}
	if err != nil {
		return 0, fmt.Errorf("update wallet: %w", err)
	}
	_, err = tx.ExecContext(tx.Ctx, `INSERT INTO wallet_ledger (ledger_id,game_id,realm_id,character_id,currency_code,actor_subject,operation_name,idempotency_key,line_item_id,amount)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, NewUUID(), tx.Realm.GameID, tx.Realm.RealmID, characterID, CurrencyCode, tx.Actor, tx.Operation, tx.Key, line, delta)
	if err != nil {
		return 0, fmt.Errorf("insert wallet ledger: %w", err)
	}
	return balance, nil
}

// Fact 记录一次性进度事实；返回 false 表示事实已存在（重复投递）。
func (tx *Tx) Fact(characterID, scope, key string) (bool, error) {
	result, err := tx.ExecContext(tx.Ctx, `INSERT INTO progress_facts (game_id,realm_id,character_id,scope,fact_key) VALUES ($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`,
		tx.Realm.GameID, tx.Realm.RealmID, characterID, scope, key)
	if err != nil {
		return false, fmt.Errorf("record progress fact: %w", err)
	}
	rows, _ := result.RowsAffected()
	return rows == 1, nil
}

// Resync 在幂等重放或提交结果未知后，从数据库重载在线角色的权威状态。
func (r *Realm) Resync(ctx context.Context, characterID string) {
	if r.players[characterID] == nil {
		return
	}
	_ = r.Tx(ctx, func(tx *Tx) error {
		record, err := tx.LoadCharacter(characterID)
		if err != nil {
			return err
		}
		tx.SyncAndNotify(record)
		return nil
	})
}

// ValidUUID 校验客户端提供的 UUID 文本，避免数据库类型转换错误。
func ValidUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for i, char := range value {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if char != '-' {
				return false
			}
			continue
		}
		if !(char >= '0' && char <= '9' || char >= 'a' && char <= 'f') {
			return false
		}
	}
	return true
}
