package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
	_ "time/tzdata" // the Vercel runtime may not ship time zone files

	"karaoke/pkg/auth"
	"karaoke/pkg/booking"
	"karaoke/pkg/memstore"
	"karaoke/pkg/sheetstore"
)

// Environment variables:
//
//	ADMIN_PIN                    staff login PIN (min 4, 6+ recommended)
//	SESSION_SECRET               random string, min 32 chars, signs the login cookie
//	TV_KEY                       secret the room TVs send to read room status
//	APP_TIMEZONE                 default Asia/Jakarta (WIB)
//	STORE                        "sheets" (default) or "memory" (local testing only)
//	SPREADSHEET_ID               ID from the spreadsheet URL
//	GOOGLE_SERVICE_ACCOUNT_JSON  service account key, raw JSON or base64
//	GOOGLE_SERVICE_ACCOUNT_FILE  path to the key file instead (local use only)
const sessionTTL = 14 * time.Hour // one long shift

// NewFromEnv builds the API from environment variables.
func NewFromEnv(ctx context.Context) (*Server, error) {
	var missing []string
	need := func(name string) string {
		v := strings.TrimSpace(os.Getenv(name))
		if v == "" {
			missing = append(missing, name)
		}
		return v
	}
	pin := need("ADMIN_PIN")
	secret := need("SESSION_SECRET")
	tvKey := need("TV_KEY")

	tzName := os.Getenv("APP_TIMEZONE")
	if tzName == "" {
		tzName = "Asia/Jakarta"
	}
	loc, err := time.LoadLocation(tzName)
	if err != nil {
		return nil, fmt.Errorf("APP_TIMEZONE: %w", err)
	}

	var store booking.Store
	switch os.Getenv("STORE") {
	case "memory":
		store = memstore.New(memstore.DemoRooms()...)
	case "", "sheets":
		id := need("SPREADSHEET_ID")
		creds := strings.TrimSpace(os.Getenv("GOOGLE_SERVICE_ACCOUNT_JSON"))
		if creds == "" {
			creds, err = credentialsFromFile(os.Getenv("GOOGLE_SERVICE_ACCOUNT_FILE"))
			if err != nil {
				return nil, err
			}
		}
		if creds == "" {
			missing = append(missing, "GOOGLE_SERVICE_ACCOUNT_JSON")
		}
		if len(missing) > 0 {
			break
		}
		credJSON, err := decodeCredentials(creds)
		if err != nil {
			return nil, err
		}
		store, err = sheetstore.New(ctx, credJSON, id, loc)
		if err != nil {
			// Do not pass on the library error: it may quote part of the key.
			return nil, errors.New("GOOGLE_SERVICE_ACCOUNT_JSON bukan service account key yang valid")
		}
	default:
		return nil, fmt.Errorf("STORE harus sheets atau memory")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("environment variable belum di-set: %s", strings.Join(missing, ", "))
	}

	a, err := auth.New(pin, secret, sessionTTL)
	if err != nil {
		return nil, err
	}
	if len(tvKey) < 16 {
		return nil, errors.New("TV_KEY minimal 16 karakter")
	}
	return New(booking.NewService(store, loc), a, tvKey), nil
}

func decodeCredentials(v string) ([]byte, error) {
	b := []byte(v)
	if !strings.HasPrefix(v, "{") {
		var err error
		if b, err = base64.StdEncoding.DecodeString(v); err != nil {
			return nil, errors.New("GOOGLE_SERVICE_ACCOUNT_JSON harus JSON atau base64 dari JSON")
		}
	}
	if !json.Valid(b) {
		return nil, errors.New("GOOGLE_SERVICE_ACCOUNT_JSON bukan JSON yang valid (terpotong saat di-paste?)")
	}
	return b, nil
}

func credentialsFromFile(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("GOOGLE_SERVICE_ACCOUNT_FILE: %w", err)
	}
	return string(b), nil
}
