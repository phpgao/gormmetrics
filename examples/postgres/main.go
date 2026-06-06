// Example: PostgreSQL with the StandardPack. Demonstrates the same
// shape as the basic MySQL example but pointing at a Postgres backend
// so users see the symmetry.
//
//	POSTGRES_DSN='host=localhost port=5432 user=u password=p dbname=d sslmode=disable' \
//	    go run ./examples/postgres
package main

import (
	"database/sql"
	"log"
	"net/http"
	"os"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/phpgao/gormmetrics"
	"github.com/phpgao/gormmetrics/postgres"
)

func main() {
	dsn := os.Getenv("POSTGRES_DSN")
	if dsn == "" {
		log.Fatal("set POSTGRES_DSN")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		log.Fatal(err)
	}
	db.SetMaxOpenConns(2)
	defer db.Close()

	c, err := gormmetrics.New(
		gormmetrics.WithDB(db),
		gormmetrics.WithScrapers(postgres.StandardPack()...),
		gormmetrics.WithLabels(map[string]string{"instance": "demo-pg"}),
	)
	if err != nil {
		log.Fatal(err)
	}

	http.Handle("/metrics", c.Handler())
	log.Println("serving /metrics on :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
