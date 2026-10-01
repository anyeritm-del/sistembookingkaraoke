// Command sheetsetup creates the Rooms and Bookings tabs with header rows in
// an existing spreadsheet. It only adds what is missing; it never changes data.
//
//	set -a; . ./.env; set +a
//	go run ./cmd/sheetsetup -check   # read only: list tabs
//	go run ./cmd/sheetsetup -seed    # create tabs, add 4 sample rooms if Rooms is new
package main

import (
	"context"
	"encoding/base64"
	"flag"
	"fmt"
	"log"
	"os"
	"slices"
	"strings"
	"time"
	_ "time/tzdata"

	"karaoke/pkg/booking"
	"karaoke/pkg/memstore"
	"karaoke/pkg/sheetstore"
)

func main() {
	seed := flag.Bool("seed", false, "add 4 sample rooms if the Rooms tab is new")
	check := flag.Bool("check", false, "only list the tabs; change nothing")
	flag.Parse()

	id := os.Getenv("SPREADSHEET_ID")
	creds, err := credentials()
	if err != nil {
		log.Fatal(err)
	}
	if id == "" || creds == "" {
		log.Fatal("set SPREADSHEET_ID and GOOGLE_SERVICE_ACCOUNT_JSON (or GOOGLE_SERVICE_ACCOUNT_FILE)")
	}
	loc, _ := time.LoadLocation("Asia/Jakarta")

	ctx := context.Background()
	st, err := sheetstore.New(ctx, []byte(creds), id, loc)
	if err != nil {
		log.Fatal(err)
	}

	tabs, err := st.TabNames(ctx)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Tab yang ada: %s\n", strings.Join(tabs, ", "))
	for _, t := range []string{sheetstore.RoomsSheet, sheetstore.BookingsSheet} {
		if slices.Contains(tabs, t) {
			fmt.Printf("  %s: sudah ada (tidak akan diubah)\n", t)
		} else {
			fmt.Printf("  %s: belum ada (akan dibuat)\n", t)
		}
	}
	if *check {
		return
	}

	var rooms []booking.Room
	if *seed {
		rooms = memstore.DemoRooms()
	}
	if err := st.EnsureSchema(ctx, rooms); err != nil {
		log.Fatal(err)
	}
	got, err := st.ListRooms(ctx)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("OK. Tab %q dan %q siap. Jumlah room: %d\n", sheetstore.RoomsSheet, sheetstore.BookingsSheet, len(got))
}

func credentials() (string, error) {
	creds := os.Getenv("GOOGLE_SERVICE_ACCOUNT_JSON")
	if creds == "" {
		if path := os.Getenv("GOOGLE_SERVICE_ACCOUNT_FILE"); path != "" {
			b, err := os.ReadFile(path)
			if err != nil {
				return "", err
			}
			return string(b), nil
		}
		return "", nil
	}
	if strings.HasPrefix(creds, "{") {
		return creds, nil
	}
	b, err := base64.StdEncoding.DecodeString(creds)
	if err != nil {
		return "", fmt.Errorf("GOOGLE_SERVICE_ACCOUNT_JSON must be JSON or base64 JSON")
	}
	return string(b), nil
}
