// Example: smallest possible setup — open a DB, install MySQL Standard
// scrapers, serve /metrics. Run with:
//
//	go run ./examples/basic
//	curl http://localhost:8080/metrics
package main

import (
	"database/sql"
	"log"
	"net/http"
	"os"

	_ "github.com/go-sql-driver/mysql"

	"github.com/phpgao/gormmetrics"
	"github.com/phpgao/gormmetrics/mysql"
)

func main() {
	dsn := os.Getenv("MYSQL_DSN")
	if dsn == "" {
		log.Fatal("set MYSQL_DSN, e.g. root:secret@tcp(localhost:3306)/mydb")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		log.Fatal(err)
	}
	// A small dedicated pool keeps metric scrapes from competing with
	// real application queries.
	db.SetMaxOpenConns(2)
	defer db.Close()

	c, err := gormmetrics.New(
		gormmetrics.WithDB(db),
		gormmetrics.WithScrapers(mysql.StandardPack()...),
		gormmetrics.WithLabels(map[string]string{"instance": "demo"}),
	)
	if err != nil {
		log.Fatal(err)
	}

	http.Handle("/metrics", c.Handler())
	log.Println("serving /metrics on :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
