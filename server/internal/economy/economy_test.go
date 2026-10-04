package economy

import (
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestValidateCommandRejectsMissingAndOutOfRangeFields(t *testing.T) {
	valid := Command{
		GameID: "game", RealmID: "realm", ActorSubject: "actor", Operation: "reward",
		IdempotencyKey: "key", CharacterID: "character", CurrencyCode: "gold",
		Amount: 1, OwnerEpoch: 0, EventType: "economy.rewarded",
	}
	tests := []struct {
		name   string
		change func(*Command)
	}{
		{"game", func(value *Command) { value.GameID = " " }},
		{"realm", func(value *Command) { value.RealmID = "" }},
		{"actor", func(value *Command) { value.ActorSubject = "\t" }},
		{"operation", func(value *Command) { value.Operation = "" }},
		{"idempotency key", func(value *Command) { value.IdempotencyKey = " " }},
		{"character", func(value *Command) { value.CharacterID = "" }},
		{"currency", func(value *Command) { value.CurrencyCode = " " }},
		{"event type", func(value *Command) { value.EventType = "" }},
		{"unsupported event type", func(value *Command) { value.EventType = "arbitrary.route" }},
		{"zero amount", func(value *Command) { value.Amount = 0 }},
		{"negative amount", func(value *Command) { value.Amount = -1 }},
		{"negative owner epoch", func(value *Command) { value.OwnerEpoch = -1 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			command := valid
			test.change(&command)
			if err := validateCommand(command); err != ErrInvalidCommand {
				t.Fatalf("validateCommand error = %v, want ErrInvalidCommand", err)
			}
		})
	}
	if err := validateCommand(valid); err != nil {
		t.Fatalf("validateCommand rejected valid command: %v", err)
	}
}

func TestCommandHashUsesOnlyCanonicalBusinessPayload(t *testing.T) {
	command := Command{CharacterID: "character", CurrencyCode: "gold", Amount: 4, OwnerEpoch: 3, EventType: "economy.rewarded"}
	first, err := commandHash(command)
	if err != nil {
		t.Fatalf("hash canonical payload: %v", err)
	}
	command.OwnerEpoch = 99
	second, err := commandHash(command)
	if err != nil {
		t.Fatalf("hash repeated canonical payload: %v", err)
	}
	if first != second {
		t.Fatal("lease epoch changed the business payload hash")
	}
	command.EventType = "economy.quest.rewarded"
	third, err := commandHash(command)
	if err != nil {
		t.Fatalf("hash changed event payload: %v", err)
	}
	if first == third {
		t.Fatal("changed event type retained the original idempotency payload hash")
	}
	command.Amount++
	fourth, err := commandHash(command)
	if err != nil {
		t.Fatalf("hash changed payload: %v", err)
	}
	if first == fourth {
		t.Fatal("changed reward amount retained the original payload hash")
	}
}

func TestCommitOutcomeClassificationPreservesUncertainServerDisconnects(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{name: "server rejected commit", err: &pgconn.PgError{Severity: "ERROR", SeverityUnlocalized: "ERROR", Code: "P0001"}, want: false},
		{name: "localized server rejection", err: &pgconn.PgError{Severity: "错误", SeverityUnlocalized: "ERROR", Code: "P0001"}, want: false},
		{name: "server disconnected during commit", err: &pgconn.PgError{Severity: "FATAL", SeverityUnlocalized: "FATAL", Code: "57P01"}, want: true},
		{name: "server IO failure", err: &pgconn.PgError{Severity: "IO", SeverityUnlocalized: "IO", Code: "08006"}, want: true},
		{name: "server cancellation during commit", err: &pgconn.PgError{Severity: "CANCEL", SeverityUnlocalized: "CANCEL", Code: "57014"}, want: true},
		{name: "transport failure", err: errors.New("connection reset"), want: true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got := isCommitOutcomeUnknown(test.err); got != test.want {
				t.Fatalf("isCommitOutcomeUnknown(%v) = %v, want %v", test.err, got, test.want)
			}
		})
	}
}

func TestUUIDAndByteEqualityHelpers(t *testing.T) {
	value, err := randomUUID()
	if err != nil {
		t.Fatalf("generate UUID: %v", err)
	}
	compact := strings.ReplaceAll(value, "-", "")
	decoded, err := hex.DecodeString(compact)
	if err != nil || len(decoded) != 16 || len(value) != 36 {
		t.Fatalf("generated UUID %q is invalid: bytes=%d error=%v", value, len(decoded), err)
	}
	if !equalBytes(nil, []byte{}) || !equalBytes([]byte("same"), []byte("same")) {
		t.Fatal("equalBytes rejected equal input")
	}
	if equalBytes([]byte("same"), []byte("different")) || equalBytes([]byte("a"), []byte("ab")) {
		t.Fatal("equalBytes accepted different input")
	}
}
