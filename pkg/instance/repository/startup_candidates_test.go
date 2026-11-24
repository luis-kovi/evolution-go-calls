package instance_repository

import (
	"database/sql/driver"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestGetStartupCandidatesQuery(t *testing.T) {
	for _, tc := range []struct {
		client string
		sql    string
		args   []interface{}
	}{
		{"", `SELECT * FROM "instances" WHERE connected = $1 OR jid <> ''`, []interface{}{true}},
		{"rekovi", `SELECT * FROM "instances" WHERE client_name = $1 AND (connected = $2 OR jid <> '')`, []interface{}{"rekovi", true}},
	} {
		sqlDB, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{})
		if err != nil {
			t.Fatal(err)
		}
		args := make([]driver.Value, len(tc.args))
		for i, a := range tc.args {
			args[i] = a
		}
		mock.ExpectQuery("^" + regexp.QuoteMeta(tc.sql) + "$").WithArgs(args...).
			WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("a"))
		repo := &instanceRepository{db: db}
		got, err := repo.GetStartupCandidates(tc.client)
		if err != nil || len(got) != 1 {
			t.Fatalf("client %q: %v, %d rows", tc.client, err, len(got))
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("client %q: %v", tc.client, err)
		}
		sqlDB.Close()
	}
}
